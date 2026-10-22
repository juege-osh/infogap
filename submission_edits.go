package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const submissionEditSchema = `
CREATE TABLE IF NOT EXISTS submission_revisions(
 id INTEGER PRIMARY KEY AUTOINCREMENT,gap_id INTEGER NOT NULL REFERENCES gaps(id),editor_id INTEGER NOT NULL REFERENCES users(id),
 base_hash TEXT NOT NULL,draft TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','rejected','approved','superseded')),
 review_note TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS submission_revision_active ON submission_revisions(gap_id) WHERE status IN ('pending','rejected');
CREATE INDEX IF NOT EXISTS submission_revision_queue ON submission_revisions(status,id);
`

func initSubmissionEdits(db *sql.DB) error {
	if err := ensureColumn(db, "gaps", "edit_version", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn(db, "gaps", "stable_code", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	_, err := db.Exec(submissionEditSchema)
	return err
}

type submissionRevision struct {
	ID         int64  `json:"id"`
	GapID      int64  `json:"gap_id"`
	EditorID   int64  `json:"-"`
	BaseHash   string `json:"-"`
	Draft      *Gap   `json:"draft"`
	Original   *Gap   `json:"original,omitempty"`
	Status     string `json:"status"`
	ReviewNote string `json:"review_note"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	EditToken  string `json:"edit_token"`
}
type submissionItem struct {
	Gap       *Gap                `json:"gap"`
	EditToken string              `json:"edit_token"`
	Revision  *submissionRevision `json:"revision"`
	Editable  bool                `json:"editable"`
}

const revisionColumns = "id,gap_id,editor_id,base_hash,draft,status,review_note,created_at,updated_at"

func scanRevision(row scanner) (*submissionRevision, error) {
	v := &submissionRevision{}
	var draft string
	if err := row.Scan(&v.ID, &v.GapID, &v.EditorID, &v.BaseHash, &draft, &v.Status, &v.ReviewNote, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(draft), &v.Draft); err != nil {
		return nil, err
	}
	if v.Draft == nil {
		return nil, fmt.Errorf("empty revision")
	}
	v.EditToken = digest(fmt.Sprintf("%d:%s:%s:%s:%s", v.ID, v.Status, v.UpdatedAt, v.ReviewNote, draft))
	return v, nil
}
func gapEditHash(g *Gap) string {
	return digest(submissionHash(g) + ":" + g.Status + ":" + g.ReleasedAt + ":" + strconv.Itoa(g.Rewarded))
}
func copyGap(g *Gap) *Gap { x := *g; x.Tags = append([]string{}, g.Tags...); return &x }
func ownGapResponse(g *Gap, u *User) *Gap {
	x := copyGap(g)
	x.Unlocked = true
	if x.SourceVisibility == "internal" && u.Role != "admin" && u.Role != "reviewer" {
		redactInternalSource(x)
	}
	return x
}
func revisionResponse(v *submissionRevision, g *Gap, u *User) *submissionRevision {
	x := *v
	x.Draft = ownGapResponse(v.Draft, u)
	x.Draft.Code = g.Code
	x.Original = ownGapResponse(g, u)
	if (g.SourceVisibility == "internal" || v.Draft.SourceVisibility == "internal") && u.Role != "admin" && u.Role != "reviewer" {
		// A later privacy change also covers an earlier public draft snapshot.
		// Scrub both the draft's old identifiers and the current source before
		// returning it to the author; the stored review copy stays unchanged.
		redactInternalSource(x.Draft)
		x.Draft.SourceName, x.Draft.SourceURL = g.SourceName, g.SourceURL
		redactInternalSource(x.Draft)
		x.Draft.SourceVisibility = "internal"
		x.ReviewNote = ""
	}
	return &x
}
func loadSubmissionItem(q rankCreditReader, id int64, u *User) (*submissionItem, *Gap, error) {
	raw, err := gapScan(q.QueryRow(gapSelect+"WHERE g.id=?", 0, 0, id), &User{Role: "admin"})
	if err != nil {
		return nil, nil, err
	}
	// This private route deliberately excludes other readers, even if unlocked.
	if u.Role != "admin" && (!raw.AuthorID.Valid || raw.AuthorID.Int64 != u.ID) {
		return nil, nil, sql.ErrNoRows
	}
	rev, err := scanRevision(q.QueryRow("SELECT "+revisionColumns+" FROM submission_revisions WHERE gap_id=? AND status IN ('pending','rejected')", id))
	if err != nil && err != sql.ErrNoRows {
		return nil, nil, err
	}
	item := &submissionItem{Gap: ownGapResponse(raw, u), Editable: has([]string{"pending", "rejected", "published"}, raw.Status)}
	rt := ""
	if rev != nil {
		rt = rev.EditToken
		item.Revision = revisionResponse(rev, raw, u)
	}
	item.EditToken = digest(gapEditHash(raw) + ":" + rt)
	return item, raw, nil
}
func (s *server) getSubmissionEdit(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	item, _, err := loadSubmissionItem(s.db, idParam(r), u)
	if err == sql.ErrNoRows {
		fail(w, 404, "投稿不存在或无权查看")
		return
	}
	if err != nil {
		fail(w, 500, "投稿读取失败")
		return
	}
	send(w, 200, item)
}
func (s *server) mySubmissions(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	where := " WHERE g.author_id=?"
	args := []any{u.ID}
	status := r.URL.Query().Get("status")
	if status != "" && status != "all" {
		if !has([]string{"pending", "rejected", "published", "disputed", "expired"}, status) {
			fail(w, 400, "投稿状态不正确")
			return
		}
		where += " AND g.status=?"
		args = append(args, status)
	}
	if category := r.URL.Query().Get("category"); category != "" && category != "all" {
		if !has(categories, category) {
			fail(w, 400, "主题不正确")
			return
		}
		where += " AND g.category=?"
		args = append(args, category)
	}
	if query := strings.TrimSpace(r.URL.Query().Get("q")); query != "" {
		if utf8.RuneCountInString(query) > 100 {
			fail(w, 400, "搜索词过长")
			return
		}
		if id, ok := gapCodeID(query); ok {
			where += " AND g.id=?"
			args = append(args, id)
		} else {
			where += " AND (" + publicTextSQL("g.title", u) + " LIKE ? OR " + publicTextSQL("g.summary", u) + " LIKE ?)"
			args = append(args, "%"+query+"%", "%"+query+"%")
		}
	}
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM gaps g"+where, args...).Scan(&total); err != nil {
		fail(w, 500, "投稿读取失败")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 10000 {
		page = 10000
	}
	params := append(append([]any{}, args...), 20, (page-1)*20)
	rows, err := s.db.Query("SELECT g.id FROM gaps g"+where+" ORDER BY g.id DESC LIMIT ? OFFSET ?", params...)
	if err != nil {
		fail(w, 500, "投稿读取失败")
		return
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, 500, "投稿读取失败")
		return
	}
	items := []*submissionItem{}
	for _, id := range ids {
		item, _, e := loadSubmissionItem(s.db, id, u)
		if e != nil {
			fail(w, 500, "投稿读取失败")
			return
		}
		items = append(items, item)
	}
	send(w, 200, map[string]any{"items": items, "total": total, "page": page})
}

type submissionEditInput struct {
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	Body      string   `json:"body"`
	Category  string   `json:"category"`
	Level     string   `json:"level"`
	Evidence  string   `json:"evidence"`
	Tags      []string `json:"tags"`
	EditToken string   `json:"edit_token"`
}

func (in *submissionEditInput) validate() string {
	in.Title = strings.TrimSpace(in.Title)
	in.Summary = strings.TrimSpace(in.Summary)
	in.Body = strings.TrimSpace(in.Body)
	if utf8.RuneCountInString(in.Title) < 8 || utf8.RuneCountInString(in.Title) > 360 || utf8.RuneCountInString(in.Summary) < 15 || utf8.RuneCountInString(in.Summary) > 300 || utf8.RuneCountInString(in.Body) < 80 || utf8.RuneCountInString(in.Body) > 15000 {
		return "标题需8–360字，摘要15–300字，正文80–15000字"
	}
	if !has(categories, in.Category) || !validLevel(in.Level) || !has([]string{"peer_reviewed", "preprint", "official", "repository", "unverified"}, in.Evidence) {
		return "主题、建议等级或证据类型不正确"
	}
	if len(in.Tags) > maxTags {
		return "每条信息最多10个标签"
	}
	for _, tag := range in.Tags {
		if utf8.RuneCountInString(tag) > 24 {
			return "标签过长"
		}
	}
	if len(in.EditToken) != 64 {
		return "请重新加载稿件后提交"
	}
	return ""
}
func queueSubmissionRevision(tx *sql.Tx, g, draft *Gap, editor int64, at string) error {
	if _, err := tx.Exec("UPDATE submission_revisions SET status='superseded',updated_at=? WHERE gap_id=? AND status IN ('pending','rejected')", at, g.ID); err != nil {
		return err
	}
	raw, err := json.Marshal(draft)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO submission_revisions(gap_id,editor_id,base_hash,draft,created_at,updated_at) VALUES(?,?,?,?,?,?)", g.ID, editor, gapEditHash(g), string(raw), at, at)
	return err
}
func (s *server) editSubmission(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	if u.TestMode {
		fail(w, 403, "测试模式不能修改正式投稿")
		return
	}
	var in submissionEditInput
	if !decode(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		fail(w, 400, msg)
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	defer tx.Rollback()
	id := idParam(r)
	item, g, err := loadSubmissionItem(tx, id, u)
	if err == sql.ErrNoRows {
		fail(w, 404, "投稿不存在或无权修改")
		return
	}
	if err != nil {
		fail(w, 500, "读取稿件失败")
		return
	}
	if !item.Editable {
		fail(w, 409, "证伪或失效记录只读，请联系站长处理")
		return
	}
	if item.EditToken != in.EditToken {
		fail(w, 409, "稿件已更新，请重新加载后修改")
		return
	}
	var dup int
	if err = tx.QueryRow("SELECT COUNT(*) FROM gaps WHERE id<>? AND lower(trim(title))=lower(?)", id, in.Title).Scan(&dup); err != nil {
		fail(w, 500, "稿件校验失败")
		return
	}
	if dup > 0 {
		fail(w, 409, "站内已有相同标题")
		return
	}
	draft := copyGap(g)
	draft.Title = in.Title
	draft.Summary = in.Summary
	draft.Body = in.Body
	draft.Category = in.Category
	draft.Level = in.Level
	draft.Evidence = in.Evidence
	draft.Tags = in.Tags
	if draft.Tags == nil {
		draft.Tags = []string{}
	}
	if g.SourceVisibility == "internal" {
		draft.Evidence = "unverified"
	}
	at := stamp(s.now())
	draft.Status = "pending"
	draft.UpdatedAt = at
	draft.ReviewNote = ""
	action := "resubmit"
	if g.Status == "published" {
		err = queueSubmissionRevision(tx, g, draft, u.ID, at)
		action = "submit_revision"
	} else {
		tags, _ := json.Marshal(draft.Tags)
		_, err = tx.Exec("UPDATE gaps SET title=?,summary=?,body=?,category=?,level=?,evidence=?,tags=?,status='pending',review_note='',updated_at=?,edit_version=edit_version+1 WHERE id=?", draft.Title, draft.Summary, draft.Body, draft.Category, draft.Level, draft.Evidence, string(tags), at, id)
	}
	if err == nil {
		before, _ := json.Marshal(map[string]any{"before": g, "proposed": draft})
		err = audit(tx, r.Context(), u.ID, action, strconv.FormatInt(id, 10), string(before), at)
	}
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "稿件未保存，请重试")
		return
	}
	item, _, err = loadSubmissionItem(s.db, id, u)
	if err != nil {
		fail(w, 500, "稿件已保存，请刷新查看")
		return
	}
	send(w, 200, item)
}

func (s *server) revisionsFor(u *User, status string) ([]*submissionRevision, error) {
	rows, err := s.db.Query("SELECT "+revisionColumns+" FROM submission_revisions WHERE status=? ORDER BY id DESC LIMIT 200", status)
	if err != nil {
		return nil, err
	}
	revisions := []*submissionRevision{}
	for rows.Next() {
		v, e := scanRevision(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		revisions = append(revisions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []*submissionRevision{}
	for _, v := range revisions {
		g, e := gapScan(s.db.QueryRow(gapSelect+"WHERE g.id=?", 0, 0, v.GapID), &User{Role: "admin"})
		if e != nil {
			return nil, e
		}
		out = append(out, revisionResponse(v, g, u))
	}
	return out, nil
}
func (s *server) listRevisions(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, false)
	if u == nil {
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	if !has([]string{"pending", "rejected", "approved", "superseded"}, status) {
		fail(w, 400, "修订状态不正确")
		return
	}
	out, err := s.revisionsFor(u, status)
	if err != nil {
		fail(w, 500, "修订稿读取失败")
		return
	}
	send(w, 200, map[string]any{"items": out})
}
func (s *server) reviewRevision(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, false)
	if u == nil {
		return
	}
	var in struct {
		Action    string `json:"action"`
		Level     string `json:"level"`
		Note      string `json:"note"`
		EditToken string `json:"edit_token"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if !has([]string{"approve", "reject"}, in.Action) || len(in.EditToken) != 64 || utf8.RuneCountInString(in.Note) > 2000 || (in.Action == "reject" && utf8.RuneCountInString(in.Note) < 3) {
		fail(w, 400, "审核操作、原因或版本不正确")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "审核失败")
		return
	}
	defer tx.Rollback()
	v, err := scanRevision(tx.QueryRow("SELECT "+revisionColumns+" FROM submission_revisions WHERE id=?", idParam(r)))
	if err != nil {
		fail(w, 404, "修订稿不存在")
		return
	}
	if v.Status != "pending" || v.EditToken != in.EditToken {
		fail(w, 409, "修订稿已更新或处理，请刷新")
		return
	}
	g, err := gapScan(tx.QueryRow(gapSelect+"WHERE g.id=?", 0, 0, v.GapID), &User{Role: "admin"})
	if err != nil {
		fail(w, 404, "原稿不存在")
		return
	}
	if g.AuthorID.Valid && g.AuthorID.Int64 == u.ID {
		fail(w, 403, "不能审核自己的投稿修订")
		return
	}
	at := stamp(s.now())
	state := "rejected"
	if in.Action == "approve" {
		if g.Status != "published" || gapEditHash(g) != v.BaseHash {
			fail(w, 409, "公开版本已改变，请作者重新提交修订")
			return
		}
		if !validLevel(in.Level) || ((in.Level == "rare" || in.Level == "exclusive") && utf8.RuneCountInString(in.Note) < 10) {
			fail(w, 400, "请选择审核等级；高级信息需至少10字核验依据")
			return
		}
		d := v.Draft
		if utf8.RuneCountInString(d.Body) < 300 || utf8.RuneCountInString(d.Body) > 1000 {
			fail(w, 400, "发布修订正文需为300–1000字，请提炼事实与限制后重投")
			return
		}
		if !chineseReadingReady(d.Title, d.Summary, d.Body) {
			fail(w, 400, "请先补齐中文标题、摘要和正文")
			return
		}
		if g.SourceVisibility == "internal" && internalSourceRemains(d) {
			fail(w, 400, "修订稿仍含内部社区名称或链接，请退回整理")
			return
		}
		var duplicate int
		if err = tx.QueryRow("SELECT COUNT(*) FROM gaps WHERE id<>? AND lower(trim(title))=lower(?)", g.ID, d.Title).Scan(&duplicate); err != nil {
			fail(w, 500, "修订校验失败")
			return
		}
		if duplicate > 0 {
			fail(w, 409, "修订标题已被其他内容使用")
			return
		}
		tags, _ := json.Marshal(d.Tags)
		// Same gap ID, release date, unlocks and original reward facts are retained.
		_, err = tx.Exec("UPDATE gaps SET title=?,summary=?,body=?,category=?,level=?,evidence=?,tags=?,review_note=?,updated_at=?,edit_version=edit_version+1,stable_code=CASE WHEN stable_code='' THEN ? ELSE stable_code END WHERE id=?", d.Title, d.Summary, d.Body, d.Category, in.Level, d.Evidence, string(tags), in.Note, at, g.Code, g.ID)
		state = "approved"
	}
	if err == nil {
		_, err = tx.Exec("UPDATE submission_revisions SET status=?,review_note=?,updated_at=? WHERE id=?", state, in.Note, at, v.ID)
	}
	if err == nil {
		note, _ := json.Marshal(map[string]any{"gap_id": g.ID, "level": in.Level, "note": in.Note, "before": g})
		err = audit(tx, r.Context(), u.ID, "revision_"+in.Action, strconv.FormatInt(v.ID, 10), string(note), at)
	}
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "修订审核未保存")
		return
	}
	send(w, 200, map[string]any{"ok": true, "gap_id": g.ID, "status": state})
}
