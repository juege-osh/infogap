package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, false)
	if u == nil {
		return
	}
	queue := []*Gap{}
	rows, e := s.db.Query(gapSelect+"WHERE g.status='pending' ORDER BY g.created_at LIMIT 200", u.ID, u.ID)
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	for rows.Next() {
		g, e := gapScan(rows, u)
		if e == nil {
			queue = append(queue, g)
		}
	}
	rows.Close()
	content := []*Gap{}
	rows, e = s.db.Query(gapSelect+"ORDER BY g.id DESC LIMIT 200", u.ID, u.ID)
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	for rows.Next() {
		g, err := gapScan(rows, u)
		if err == nil {
			content = append(content, g)
		}
	}
	rows.Close()
	users := []*User{}
	invites := []map[string]any{}
	if u.Role == "admin" {
		rows, e = s.db.Query("SELECT " + userCols + " FROM users WHERE id NOT IN (SELECT user_id FROM test_accounts) ORDER BY id DESC LIMIT 500")
		if e != nil {
			fail(w, 500, "读取失败")
			return
		}
		for rows.Next() {
			x, e := userScan(rows)
			if e == nil {
				users = append(users, s.decorate(x))
			}
		}
		rows.Close()
		rows, e = s.db.Query("SELECT id,code,max_uses,uses,expires_at,active FROM invites ORDER BY id DESC LIMIT 100")
		if e != nil {
			fail(w, 500, "读取失败")
			return
		}
		for rows.Next() {
			var id int64
			var code, exp string
			var maxuses, uses int
			var active bool
			if rows.Scan(&id, &code, &maxuses, &uses, &exp, &active) == nil {
				invites = append(invites, map[string]any{"id": id, "code": code, "max_uses": maxuses, "uses": uses, "expires_at": exp, "active": active})
			}
		}
		rows.Close()
	}
	sources := []map[string]any{}
	rows, e = s.db.Query("SELECT id,name,url,kind,category,enabled,last_attempt,last_success,last_error,last_count FROM sources ORDER BY name")
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	for rows.Next() {
		var id, name, link, kind, cat, errstr string
		var attempt, success sql.NullString
		var enabled bool
		var count int
		if rows.Scan(&id, &name, &link, &kind, &cat, &enabled, &attempt, &success, &errstr, &count) == nil {
			sources = append(sources, map[string]any{"id": id, "name": name, "url": link, "kind": kind, "category": cat, "enabled": enabled, "last_attempt": attempt.String, "last_success": success.String, "last_error": errstr, "last_count": count})
		}
	}
	rows.Close()
	reports := []map[string]any{}
	rows, e = s.db.Query("SELECT r.id,r.gap_id,r.reason,r.created_at,r.status,g.title,u.username FROM reports r JOIN gaps g ON g.id=r.gap_id JOIN users u ON u.id=r.user_id WHERE r.status='open' ORDER BY r.id DESC LIMIT 100")
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	for rows.Next() {
		var id, gap int64
		var reason, at, status, title, name string
		if rows.Scan(&id, &gap, &reason, &at, &status, &title, &name) == nil {
			reports = append(reports, map[string]any{"id": id, "gap_id": gap, "reason": reason, "created_at": at, "status": status, "title": title, "username": name})
		}
	}
	rows.Close()
	summaryJobs := map[string]int{"pending": 0, "completed": 0, "failed": 0, "too_long": 0, "edited": 0, "processing": 0}
	rows, e = s.db.Query("SELECT state,COUNT(*) FROM summary_jobs GROUP BY state")
	if e != nil {
		fail(w, 500, "读取精炼状态失败")
		return
	}
	for rows.Next() {
		var state string
		var count int
		if rows.Scan(&state, &count) == nil {
			if _, known := summaryJobs[state]; known {
				summaryJobs[state] = count
			}
		}
	}
	rows.Close()
	send(w, 200, map[string]any{"stats": s.stats(u), "queue": queue, "content": content, "users": users, "invites": invites, "sources": sources, "reports": reports, "summary_jobs": summaryJobs})
}
func (s *server) newInvite(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, true)
	if u == nil {
		return
	}
	var in struct {
		MaxUses int `json:"max_uses"`
		Days    int `json:"days"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.MaxUses == 0 {
		in.MaxUses = 1
	}
	if in.Days == 0 {
		in.Days = 7
	}
	if in.MaxUses < 1 || in.MaxUses > 100 || in.Days < 1 || in.Days > 90 {
		fail(w, 400, "使用次数限 1–100，期限限 1–90 天")
		return
	}
	code := strings.ToUpper(token(8))
	expires := stamp(s.now().Add(time.Duration(in.Days) * 24 * time.Hour))
	_, e := s.db.Exec("INSERT INTO invites(code,max_uses,expires_at,created_by,created_at) VALUES(?,?,?,?,?)", code, in.MaxUses, expires, u.ID, stamp(s.now()))
	if e != nil {
		fail(w, 500, "创建失败")
		return
	}
	send(w, 201, map[string]any{"code": code, "expires_at": expires})
}
func (s *server) revokeInvite(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, true)
	if u == nil {
		return
	}
	res, e := s.db.Exec("UPDATE invites SET active=0 WHERE id=?", idParam(r))
	if e != nil {
		fail(w, 500, "操作失败")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "邀请码不存在")
		return
	}
	send(w, 200, map[string]bool{"ok": true})
}
func (s *server) review(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, false)
	if u == nil {
		return
	}
	var in struct {
		Action string `json:"action"`
		Level  string `json:"level"`
		Note   string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(in.Note) > 2000 || !has([]string{"approve", "reject", "dispute", "expire"}, in.Action) {
		fail(w, 400, "审核操作不正确")
		return
	}
	if in.Action != "approve" && utf8.RuneCountInString(in.Note) < 3 {
		fail(w, 400, "请说明具体原因")
		return
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, "审核失败")
		return
	}
	defer tx.Rollback()
	id := idParam(r)
	g, e := gapScan(tx.QueryRow(gapSelect+"WHERE g.id=?", u.ID, u.ID, id), u)
	if e != nil {
		fail(w, 404, "信息不存在")
		return
	}
	if g.AuthorID.Valid && g.AuthorID.Int64 == u.ID {
		fail(w, 403, "不能审核自己的投稿，请由另一位审核员复核")
		return
	}
	at := stamp(s.now())
	switch in.Action {
	case "approve":
		if g.Status != "pending" && g.Status != "rejected" {
			fail(w, 409, "该信息已经处理，请刷新")
			return
		}
		if !validLevel(in.Level) {
			fail(w, 400, "请选择内容等级")
			return
		}
		if g.SourceVisibility == "internal" && internalSourceRemains(g) {
			fail(w, 400, "内部线索稿仍含社区名称或链接，请整理标题、摘要、正文与标签后再发布；原始来源会保留在后台")
			return
		}
		if in.Level == "rare" || in.Level == "exclusive" {
			if utf8.RuneCountInString(in.Note) < 10 {
				fail(w, 400, "高级信息需填写至少 10 字的核验依据")
				return
			}
		}
		_, e = tx.Exec("UPDATE gaps SET status='published',level=?,review_note=?,updated_at=?,released_at=CASE WHEN released_at='' THEN ? ELSE released_at END WHERE id=?", in.Level, in.Note, at, at, id)
		if e == nil && g.Rewarded == 0 && g.AuthorID.Valid {
			points := map[string]int{"ordinary": 20, "featured": 80, "rare": 200, "exclusive": 500}[in.Level]
			featured, rare := 0, 0
			quality := ""
			if in.Level != "ordinary" {
				featured = 1
				quality = at
			}
			if in.Level == "rare" || in.Level == "exclusive" {
				rare = 1
			}
			_, e = tx.Exec("UPDATE users SET credits=credits+100,points=points+?,contributions=contributions+1,featured_contributions=featured_contributions+?,rare_contributions=rare_contributions+?,last_quality_at=CASE WHEN ?='' THEN last_quality_at ELSE ? END WHERE id=?", points, featured, rare, quality, quality, g.AuthorID.Int64)
			if e == nil {
				_, e = tx.Exec("INSERT INTO ledger(user_id,gap_id,kind,amount,created_at,note) VALUES(?,?,'contribution',100,?,'有效贡献通过审核')", g.AuthorID.Int64, id, at)
			}
			if e == nil {
				_, e = tx.Exec("INSERT INTO contribution_rewards(gap_id,user_id,points,featured,rare,awarded_at) VALUES(?,?,?,?,?,?)", id, g.AuthorID.Int64, points, featured, rare, at)
			}
			if e == nil {
				_, e = tx.Exec("UPDATE gaps SET rewarded=1 WHERE id=?", id)
			}
		}
	case "reject":
		if g.Status != "pending" && g.Status != "rejected" {
			fail(w, 409, "已发布内容请使用证伪或失效操作")
			return
		}
		_, e = tx.Exec("UPDATE gaps SET status='rejected',review_note=?,updated_at=? WHERE id=?", in.Note, at, id)
	case "expire":
		if g.Status != "published" {
			fail(w, 409, "仅可标记已发布的信息")
			return
		}
		_, e = tx.Exec("UPDATE gaps SET status='expired',review_note=?,updated_at=? WHERE id=?", in.Note, at, id)
	case "dispute":
		if g.Status != "published" && g.Status != "expired" {
			fail(w, 409, "该信息无法重复证伪")
			return
		}
		_, e = tx.Exec("UPDATE gaps SET status='disputed',review_note=?,updated_at=? WHERE id=?", in.Note, at, id)
		if e == nil {
			e = refundReadingCredits(tx, id, s.now())
			if e == nil {
				_, e = tx.Exec("UPDATE reports SET status='resolved' WHERE gap_id=?", id)
			}
			if e == nil && g.Rewarded == 1 && g.AuthorID.Valid {
				points, featured, rare := map[string]int{"ordinary": 20, "featured": 80, "rare": 200, "exclusive": 500}[g.Level], 0, 0
				if g.Level != "ordinary" {
					featured = 1
				}
				if g.Level == "rare" || g.Level == "exclusive" {
					rare = 1
				}
				// Reward facts survive later editorial changes to the displayed level.
				err := tx.QueryRow("SELECT points,featured,rare FROM contribution_rewards WHERE gap_id=?", id).Scan(&points, &featured, &rare)
				if err != nil && err != sql.ErrNoRows {
					e = err
				}
				var balance int
				if e == nil {
					e = tx.QueryRow("SELECT credits FROM users WHERE id=?", g.AuthorID.Int64).Scan(&balance)
				}
				debit := min(100, balance)
				if e == nil {
					_, e = tx.Exec("UPDATE users SET credits=credits-?,points=max(0,points-?),contributions=max(0,contributions-1),featured_contributions=max(0,featured_contributions-?),rare_contributions=max(0,rare_contributions-?) WHERE id=?", debit, points, featured, rare, g.AuthorID.Int64)
				}
				if e == nil {
					_, e = tx.Exec("UPDATE contribution_rewards SET revoked=1 WHERE gap_id=?", id)
				}
				if e == nil {
					_, e = tx.Exec("UPDATE users SET last_quality_at=COALESCE((SELECT MAX(awarded_at) FROM contribution_rewards WHERE user_id=? AND featured=1 AND revoked=0),'') WHERE id=?", g.AuthorID.Int64, g.AuthorID.Int64)
				}
				if e == nil {
					_, e = tx.Exec("INSERT INTO ledger(user_id,gap_id,kind,amount,created_at,note) VALUES(?,?,'reward_revoke',?,?,'信息被证伪，撤销积分与贡献资格，并收回剩余奖励额度')", g.AuthorID.Int64, id, -debit, at)
				}
				if e == nil {
					_, e = tx.Exec("UPDATE gaps SET rewarded=-1 WHERE id=?", id)
				}
			}
		}
	}
	if e == nil {
		e = audit(tx, r.Context(), u.ID, in.Action, strconv.FormatInt(id, 10), in.Note, at)
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "审核未保存，请稍后重试")
		return
	}
	send(w, 200, map[string]bool{"ok": true})
}
func (s *server) manageUser(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, true)
	if u == nil {
		return
	}
	var in struct {
		Role           *string `json:"role"`
		Disabled       *bool   `json:"disabled"`
		MembershipDays *int    `json:"membership_days"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Role == nil && in.Disabled == nil && in.MembershipDays == nil {
		fail(w, 400, "没有要修改的内容")
		return
	}
	if in.Role != nil && *in.Role != "member" && *in.Role != "reviewer" {
		fail(w, 400, "只能指定成员或审核员")
		return
	}
	if in.MembershipDays != nil && (*in.MembershipDays < 0 || *in.MembershipDays > 3660) {
		fail(w, 400, "会员天数需为0至3660，0表示撤销")
		return
	}
	id := idParam(r)
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, "修改失败")
		return
	}
	defer tx.Rollback()
	var email string
	if e = tx.QueryRow("SELECT email FROM users WHERE id=?", id).Scan(&email); e != nil {
		fail(w, 404, "用户不存在")
		return
	}
	if email == founderEmail {
		fail(w, 403, "不能修改站长权限或停用站长")
		return
	}
	if in.Role != nil {
		_, e = tx.Exec("UPDATE users SET role=? WHERE id=?", *in.Role, id)
	}
	if e == nil && in.Disabled != nil {
		_, e = tx.Exec("UPDATE users SET disabled=? WHERE id=?", *in.Disabled, id)
		if e == nil && *in.Disabled {
			_, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", id)
		}
	}
	if e == nil && in.MembershipDays != nil {
		var before string
		e = tx.QueryRow("SELECT membership_until FROM users WHERE id=?", id).Scan(&before)
		after := ""
		if *in.MembershipDays > 0 {
			base := s.now()
			until, err := time.Parse(time.RFC3339, before)
			if err == nil && until.After(base) {
				base = until
			}
			after = stamp(base.Add(time.Duration(*in.MembershipDays) * 24 * time.Hour))
		}
		if e == nil {
			_, e = tx.Exec("UPDATE users SET membership_until=? WHERE id=?", after, id)
		}
		if e == nil {
			note, _ := json.Marshal(map[string]any{"days": *in.MembershipDays, "before": before, "after": after})
			e = audit(tx, r.Context(), u.ID, "membership_change", strconv.FormatInt(id, 10), string(note), stamp(s.now()))
		}
	}
	if e == nil {
		e = audit(tx, r.Context(), u.ID, "manage_user", strconv.FormatInt(id, 10), "调整用户角色、状态或功能会员期限", stamp(s.now()))
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "修改失败")
		return
	}
	send(w, 200, map[string]bool{"ok": true})
}
func (s *server) manageSource(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, true)
	if u == nil {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		fail(w, 400, "请指定启用状态")
		return
	}
	res, e := s.db.Exec("UPDATE sources SET enabled=? WHERE id=?", *in.Enabled, r.PathValue("id"))
	if e != nil {
		fail(w, 500, "修改失败")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "来源不存在")
		return
	}
	send(w, 200, map[string]bool{"ok": true})
}

func (s *server) editGap(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, false)
	if u == nil {
		return
	}
	var in struct {
		Title            string   `json:"title"`
		Summary          string   `json:"summary"`
		Body             string   `json:"body"`
		Category         string   `json:"category"`
		Evidence         string   `json:"evidence"`
		Tags             []string `json:"tags"`
		SourceVisibility *string  `json:"source_visibility"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Summary = strings.TrimSpace(in.Summary)
	in.Body = strings.TrimSpace(in.Body)
	if utf8.RuneCountInString(in.Title) < 8 || utf8.RuneCountInString(in.Title) > 120 || utf8.RuneCountInString(in.Summary) < 15 || utf8.RuneCountInString(in.Summary) > 300 || utf8.RuneCountInString(in.Body) < 80 || utf8.RuneCountInString(in.Body) > 15000 {
		fail(w, 400, "标题需 8–120 字，摘要 15–300 字，正文 80–15000 字")
		return
	}
	if !has(categories, in.Category) || !has([]string{"peer_reviewed", "preprint", "official", "repository", "unverified"}, in.Evidence) {
		fail(w, 400, "分类、证据类型或标签不正确")
		return
	}
	if in.SourceVisibility != nil && !has([]string{"public", "internal"}, *in.SourceVisibility) {
		fail(w, 400, "来源可见性不正确")
		return
	}
	if len(in.Tags) > maxTags {
		fail(w, 400, "每条信息最多 10 个标签")
		return
	}
	for _, tag := range in.Tags {
		if utf8.RuneCountInString(tag) > 24 {
			fail(w, 400, "标签过长")
			return
		}
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, "保存失败")
		return
	}
	defer tx.Rollback()
	id := idParam(r)
	g, e := gapScan(tx.QueryRow(gapSelect+"WHERE g.id=?", u.ID, u.ID, id), u)
	if e != nil {
		fail(w, 404, "信息不存在")
		return
	}
	if g.Status == "disputed" {
		fail(w, 409, "证伪记录需保留，请另建新稿并说明更正依据")
		return
	}
	tags, _ := json.Marshal(in.Tags)
	if in.Tags == nil {
		tags = []byte("[]")
	}
	at := stamp(s.now())
	visibility := g.SourceVisibility
	if in.SourceVisibility != nil {
		visibility = *in.SourceVisibility
	}
	_, e = tx.Exec("UPDATE gaps SET title=?,summary=?,body=?,category=?,evidence=?,tags=?,source_visibility=?,status='pending',updated_at=? WHERE id=?", in.Title, in.Summary, in.Body, in.Category, in.Evidence, string(tags), visibility, at, id)
	if e == nil {
		before, _ := json.Marshal(map[string]any{"title": g.Title, "summary": g.Summary, "body": g.Body, "category": g.Category, "evidence": g.Evidence, "tags": g.Tags, "status": g.Status, "source_visibility": g.SourceVisibility})
		e = audit(tx, r.Context(), u.ID, "edit_gap", strconv.FormatInt(id, 10), string(before), at)
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "内容未保存，请稍后重试")
		return
	}
	send(w, 200, map[string]any{"ok": true, "status": "pending"})
}
