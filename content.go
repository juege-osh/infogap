package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxTags = 10

type Gap struct {
	ID               int64         `json:"id"`
	Code             string        `json:"code"`
	Title            string        `json:"title"`
	Summary          string        `json:"summary"`
	Body             string        `json:"body,omitempty"`
	Category         string        `json:"category"`
	Level            string        `json:"level"`
	Evidence         string        `json:"evidence"`
	SourceName       string        `json:"source_name"`
	SourceURL        string        `json:"source_url"`
	SourceVisibility string        `json:"source_visibility"`
	PublishedAt      string        `json:"published_at"`
	ReleasedAt       string        `json:"released_at"`
	CreatedAt        string        `json:"created_at"`
	UpdatedAt        string        `json:"updated_at"`
	Status           string        `json:"status"`
	Tags             []string      `json:"tags"`
	Author           string        `json:"author"`
	AuthorID         sql.NullInt64 `json:"-"`
	Unlocked         bool          `json:"unlocked"`
	UnlockCost       int           `json:"unlock_cost"`
	Saved            bool          `json:"saved"`
	ReviewNote       string        `json:"review_note,omitempty"`
	Rewarded         int           `json:"-"`
}

const gapSelect = `SELECT g.id,g.title,g.summary,g.body,g.category,g.level,g.evidence,g.source_name,g.source_url,g.source_visibility,g.published_at,g.released_at,g.created_at,g.updated_at,g.status,g.tags,g.author_id,g.review_note,g.rewarded,COALESCE(u.username,'InfoGap 编辑部'),EXISTS(SELECT 1 FROM unlocks un WHERE un.gap_id=g.id AND un.user_id=?),EXISTS(SELECT 1 FROM saves sv WHERE sv.gap_id=g.id AND sv.user_id=?) FROM gaps g LEFT JOIN users u ON g.author_id=u.id `

func gapScan(r scanner, u *User) (*Gap, error) {
	g := &Gap{}
	var tags string
	err := r.Scan(&g.ID, &g.Title, &g.Summary, &g.Body, &g.Category, &g.Level, &g.Evidence, &g.SourceName, &g.SourceURL, &g.SourceVisibility, &g.PublishedAt, &g.ReleasedAt, &g.CreatedAt, &g.UpdatedAt, &g.Status, &tags, &g.AuthorID, &g.ReviewNote, &g.Rewarded, &g.Author, &g.Unlocked, &g.Saved)
	if err != nil {
		return nil, err
	}
	g.Code = gapCode(g.ID, g.Level, g.Category)
	g.Tags = []string{}
	g.UnlockCost = unlockCosts[g.Level]
	_ = json.Unmarshal([]byte(tags), &g.Tags)
	if g.Tags == nil {
		g.Tags = []string{}
	}
	owner := u != nil && g.AuthorID.Valid && g.AuthorID.Int64 == u.ID
	staff := u != nil && (u.Role == "admin" || u.Role == "reviewer")
	g.Unlocked = g.Unlocked || owner || staff
	if !g.Unlocked {
		g.Body = ""
	}
	if !owner && !staff && g.Status != "disputed" {
		g.ReviewNote = ""
	}
	if g.SourceVisibility == "internal" && !staff {
		redactInternalSource(g)
	}
	redactLockedSource(g)
	return g, nil
}
func uid(u *User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}
func (s *server) canSee(g *Gap, u *User) bool {
	if g.Unlocked || (u != nil && ((g.AuthorID.Valid && g.AuthorID.Int64 == u.ID) || u.Role == "admin" || u.Role == "reviewer")) {
		return true
	}
	if g.Status != "published" && g.Status != "disputed" && g.Status != "expired" {
		return false
	}
	at, err := time.Parse(time.RFC3339, g.ReleasedAt)
	return err == nil && !at.After(s.now().Add(-time.Duration(s.delayHours(u))*time.Hour))
}
func (s *server) delayHours(u *User) int {
	if u == nil {
		return 72
	}
	return s.decorate(u).Entitlements.DelayHours
}
func (s *server) visibilityClause(u *User) (string, []any) {
	if u != nil && (u.Role == "admin" || u.Role == "reviewer") {
		return "", nil
	}
	return " AND (g.author_id=? OR EXISTS(SELECT 1 FROM unlocks vis WHERE vis.gap_id=g.id AND vis.user_id=?) OR (g.released_at<>'' AND julianday(g.released_at)<=julianday(?)))", []any{uid(u), uid(u), stamp(s.now().Add(-time.Duration(s.delayHours(u)) * time.Hour))}
}
func (s *server) oneGap(id int64, u *User) (*Gap, error) {
	return gapScan(s.db.QueryRow(gapSelect+"WHERE g.id=?", uid(u), uid(u), id), u)
}
func (s *server) listGaps(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	view := r.URL.Query().Get("view")
	if view != "" && view != "all" && u == nil {
		fail(w, 401, "请先登录")
		return
	}
	where := " WHERE g.status IN ('published','disputed','expired')"
	args := []any{}
	if status := r.URL.Query().Get("status"); status != "" {
		if u == nil || (u.Role != "admin" && u.Role != "reviewer") {
			fail(w, 403, "状态筛选仅供审核人员使用")
			return
		}
		if status == "all" {
			where = " WHERE 1=1"
		} else if has([]string{"pending", "published", "rejected", "expired", "disputed"}, status) {
			where = " WHERE g.status=?"
			args = append(args, status)
		} else {
			fail(w, 400, "内容状态不正确")
			return
		}
	}
	switch view {
	case "mine":
		where = " WHERE g.author_id=?"
		args = []any{u.ID}
	case "saved":
		where += " AND EXISTS(SELECT 1 FROM saves x WHERE x.gap_id=g.id AND x.user_id=?)"
		args = append(args, u.ID)
	case "unlocked":
		where = " WHERE EXISTS(SELECT 1 FROM unlocks x WHERE x.gap_id=g.id AND x.user_id=?)"
		args = []any{u.ID}
	case "subscribed":
		if !u.Entitlements.Subscriptions {
			fail(w, 403, "订阅功能需黄金段位或有效功能会员")
			return
		}
		where = " WHERE g.status='published' AND EXISTS(SELECT 1 FROM (SELECT kind,value FROM subscriptions WHERE user_id=? ORDER BY id LIMIT ?) sub WHERE (sub.kind='category' AND sub.value=g.category) OR (sub.kind='tag' AND EXISTS(SELECT 1 FROM json_each(CASE WHEN json_valid(g.tags) THEN g.tags ELSE '[]' END) jt WHERE " + publicTextSQL("jt.value", u) + "=sub.value COLLATE NOCASE)))"
		args = []any{u.ID, u.Entitlements.SubscriptionLimit}
	}
	visible, visibleArgs := s.visibilityClause(u)
	where += visible
	args = append(args, visibleArgs...)
	for _, key := range []string{"category", "level"} {
		if v := r.URL.Query().Get(key); v != "" && v != "all" {
			where += " AND g." + key + "=?"
			args = append(args, v)
		}
	}
	travelWhere, travelArgs, travelErr := travelFilter(r.URL.Query(), u)
	if travelErr != nil {
		fail(w, 400, travelErr.Error())
		return
	}
	where += travelWhere
	args = append(args, travelArgs...)
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		if utf8.RuneCountInString(q) > 100 {
			fail(w, 400, "搜索词过长")
			return
		}
		if id, ok := gapCodeID(q); ok {
			// A code only narrows the already-authorized list; it does not bypass
			// status, release delay, subscriptions, or any other active filter.
			where += " AND g.id=?"
			args = append(args, id)
		} else {
			where += " AND (" + publicTextSQL("g.title", u) + " LIKE ? OR " + publicTextSQL("g.summary", u) + " LIKE ? OR EXISTS(SELECT 1 FROM json_each(CASE WHEN json_valid(g.tags) THEN g.tags ELSE '[]' END) search_tag WHERE " + publicTextSQL("search_tag.value", u) + " LIKE ?))"
			p := "%" + q + "%"
			args = append(args, p, p, p)
		}
	}
	var total int
	err := s.db.QueryRow("SELECT COUNT(*) FROM gaps g"+where, args...).Scan(&total)
	if err != nil {
		fail(w, 500, "读取失败")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 10000 {
		page = 10000
	}
	sort := "g.released_at DESC,g.published_at DESC,g.id DESC"
	if r.URL.Query().Get("sort") == "value" {
		sort = "CASE g.level WHEN 'exclusive' THEN 4 WHEN 'rare' THEN 3 WHEN 'featured' THEN 2 ELSE 1 END DESC,g.released_at DESC,g.published_at DESC,g.id DESC"
	}
	params := []any{uid(u), uid(u)}
	params = append(params, args...)
	params = append(params, 20, (page-1)*20)
	rows, err := s.db.Query(gapSelect+where+" ORDER BY "+sort+" LIMIT ? OFFSET ?", params...)
	if err != nil {
		fail(w, 500, "读取失败")
		return
	}
	defer rows.Close()
	out := []*Gap{}
	for rows.Next() {
		g, e := gapScan(rows, u)
		if e != nil {
			fail(w, 500, "读取失败")
			return
		}
		out = append(out, g)
	}
	send(w, 200, map[string]any{"items": out, "total": total, "page": page})
}
func (s *server) getGap(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	g, e := s.oneGap(idParam(r), u)
	if e != nil || !s.canSee(g, u) {
		fail(w, 404, "信息不存在或尚未发布")
		return
	}
	send(w, 200, g)
}
func (s *server) unlock(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	isTestMode := u.TestMode
	id := idParam(r)
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "解锁失败")
		return
	}
	defer tx.Rollback()
	u, err = userScan(tx.QueryRow("SELECT "+userCols+" FROM users WHERE id=? AND disabled=0", u.ID))
	if err != nil {
		fail(w, 401, "请重新登录")
		return
	}
	u.TestMode = isTestMode
	s.decorate(u)
	now := s.now()
	day := dayKey(now)
	// Fix the charge to one day even when this request straddles local midnight.
	u.DailyRemaining = u.DailyAllowance
	if u.Day == day {
		u.DailyRemaining = max(0, u.DailyAllowance-u.DailyUsed)
	}
	if err = expireRankCredits(tx, u.ID, now); err == nil {
		err = loadRankCredits(tx, u, now)
	}
	if err != nil {
		fail(w, 500, "赠分余额读取失败")
		return
	}
	g, err := gapScan(tx.QueryRow(gapSelect+"WHERE g.id=?", u.ID, u.ID, id), u)
	if err != nil || !s.canSee(g, u) {
		fail(w, 404, "信息不存在")
		return
	}
	if g.Unlocked {
		_ = tx.Rollback()
		send(w, 200, map[string]any{"gap": g, "user": u})
		return
	}
	if g.Status != "published" {
		fail(w, 409, "该信息已失效或待核验，暂不开放新的解锁")
		return
	}
	if g.Level == "rare" && u.TierIndex < 2 {
		fail(w, 403, "稀有信息需要白银及以上段位")
		return
	}
	if g.Level == "exclusive" && u.TierIndex < 6 {
		fail(w, 403, "独家信息需要钻石及以上段位")
		return
	}
	totalCost := unlockCosts[g.Level]
	if totalCost <= 0 {
		fail(w, 409, "内容等级尚未确定，暂不能解锁")
		return
	}
	dailyCost := min(totalCost, u.DailyRemaining)
	rankCost := min(totalCost-dailyCost, u.RankCredits)
	cost := totalCost - dailyCost - rankCost
	kind := "credit"
	if u.Credits < cost {
		fail(w, 402, "阅读额度不足。有效贡献按审核等级可获得 100–800 点，也可购买阅读额度。")
		return
	}
	used := u.DailyUsed
	if u.Day != day {
		used = 0
	}
	if dailyCost > 0 || rankCost > 0 {
		kind = "mixed"
	}
	if dailyCost == totalCost {
		kind = "daily"
	} else if rankCost == totalCost {
		kind = "rank"
	}
	_, err = tx.Exec("UPDATE users SET credits=credits-?,day_key=?,daily_used=? WHERE id=? AND credits>=?", cost, day, used+dailyCost, u.ID, cost)
	if err == nil {
		_, err = tx.Exec("INSERT INTO unlocks(user_id,gap_id,cost,kind,created_at,daily_cost,day_key,rank_cost) VALUES(?,?,?,?,?,?,?,?)", u.ID, id, cost, kind, stamp(now), dailyCost, day, rankCost)
	}
	if err == nil {
		err = spendRankCredits(tx, u.ID, id, rankCost, now)
	}
	if err == nil && dailyCost > 0 {
		_, err = tx.Exec("INSERT INTO ledger(user_id,gap_id,kind,amount,created_at,note) VALUES(?,?,?,?,?,?)", u.ID, id, "daily_unlock", -dailyCost, stamp(now), "使用今日赠送额度解锁")
	}
	if err == nil && cost > 0 {
		_, err = tx.Exec("INSERT INTO ledger(user_id,gap_id,kind,amount,created_at,note) VALUES(?,?,?,?,?,?)", u.ID, id, "unlock", -cost, stamp(now), "使用长期阅读额度解锁")
	}
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "解锁失败，未扣除额度")
		return
	}
	u, _ = userScan(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", u.ID))
	u.TestMode = isTestMode
	g, _ = s.oneGap(id, u)
	send(w, 200, map[string]any{"gap": g, "user": s.decorateWithRank(u)})
}
func (s *server) saveGap(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	g, e := s.oneGap(idParam(r), u)
	if e != nil || !s.canSee(g, u) {
		fail(w, 404, "信息不存在")
		return
	}
	if g.Saved {
		_, e = s.db.Exec("DELETE FROM saves WHERE user_id=? AND gap_id=?", u.ID, g.ID)
	} else {
		_, e = s.db.Exec("INSERT OR IGNORE INTO saves(user_id,gap_id,created_at) VALUES(?,?,?)", u.ID, g.ID, stamp(s.now()))
	}
	if e != nil {
		fail(w, 500, "收藏失败")
		return
	}
	send(w, 200, map[string]bool{"saved": !g.Saved})
}
func (s *server) reportGap(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if utf8.RuneCountInString(in.Reason) < 5 || utf8.RuneCountInString(in.Reason) > 1000 {
		fail(w, 400, "请填写 5–1000 字的具体原因或证据")
		return
	}
	g, e := s.oneGap(idParam(r), u)
	if e != nil || !s.canSee(g, u) {
		fail(w, 404, "信息不存在")
		return
	}
	_, e = s.db.Exec("INSERT INTO reports(user_id,gap_id,reason,created_at) VALUES(?,?,?,?) ON CONFLICT(user_id,gap_id) DO UPDATE SET reason=excluded.reason,created_at=excluded.created_at,status='open'", u.ID, g.ID, in.Reason, stamp(s.now()))
	if e != nil {
		fail(w, 500, "提交失败")
		return
	}
	send(w, 200, map[string]bool{"ok": true})
}
func has(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
func validLevel(v string) bool { return has([]string{"ordinary", "featured", "rare", "exclusive"}, v) }

type submission struct {
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Body       string   `json:"body"`
	Category   string   `json:"category"`
	SourceURL  string   `json:"source_url"`
	SourceName string   `json:"source_name"`
	Level      string   `json:"level"`
	Evidence   string   `json:"evidence"`
	Tags       []string `json:"tags"`
}

func (s *server) submit(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	var in submission
	if !decode(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Summary = strings.TrimSpace(in.Summary)
	in.Body = strings.TrimSpace(in.Body)
	in.SourceURL = strings.TrimSpace(in.SourceURL)
	in.SourceName = strings.TrimSpace(in.SourceName)
	if utf8.RuneCountInString(in.Title) < 8 || utf8.RuneCountInString(in.Title) > 120 || utf8.RuneCountInString(in.Summary) < 15 || utf8.RuneCountInString(in.Summary) > 300 || utf8.RuneCountInString(in.Body) < 80 || utf8.RuneCountInString(in.Body) > 15000 {
		fail(w, 400, "标题需 8–120 字，摘要 15–300 字，正文 80–15000 字")
		return
	}
	if !allowedURL(in.SourceURL) || len(in.SourceURL) > 2000 || in.SourceName == "" || utf8.RuneCountInString(in.SourceName) > 80 || !has(categories, in.Category) || !validLevel(in.Level) || !has([]string{"peer_reviewed", "preprint", "official", "repository", "unverified"}, in.Evidence) {
		fail(w, 400, "请检查分类、证据类型和来源链接")
		return
	}
	if len(in.Tags) > maxTags {
		fail(w, 400, "每条信息最多 10 个标签")
		return
	}
	for _, t := range in.Tags {
		if utf8.RuneCountInString(t) > 24 {
			fail(w, 400, "标签过长")
			return
		}
	}
	parsed, _ := url.Parse(in.SourceURL)
	parsed.Fragment = ""
	in.SourceURL = parsed.String()
	visibility := defaultSourceVisibility(in.SourceURL)
	if visibility == "internal" {
		in.Evidence = "unverified"
	}
	tags, _ := json.Marshal(in.Tags)
	if in.Tags == nil {
		tags = []byte("[]")
	}
	now := stamp(s.now())
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "投稿失败")
		return
	}
	defer tx.Rollback()
	var n int
	_ = tx.QueryRow("SELECT COUNT(*) FROM gaps WHERE author_id=? AND created_at>?", u.ID, stamp(s.now().Add(-24*time.Hour))).Scan(&n)
	if n >= 10 {
		fail(w, 429, "今天已提交 10 条，请等待审核后再来")
		return
	}
	_ = tx.QueryRow("SELECT COUNT(*) FROM gaps WHERE lower(trim(title))=lower(?) OR source_url=?", in.Title, in.SourceURL).Scan(&n)
	if n > 0 {
		fail(w, 409, "站内已有相同标题或来源，请补充现有信息或联系审核员")
		return
	}
	res, err := tx.Exec("INSERT INTO gaps(title,summary,body,category,level,evidence,source_name,source_url,source_visibility,published_at,created_at,updated_at,status,tags,author_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'pending',?,?)", in.Title, in.Summary, in.Body, in.Category, in.Level, in.Evidence, in.SourceName, in.SourceURL, visibility, "", now, now, string(tags), u.ID)
	if err != nil {
		fail(w, 409, "投稿未保存，来源可能已存在")
		return
	}
	id, _ := res.LastInsertId()
	if tx.Commit() != nil {
		fail(w, 500, "投稿失败")
		return
	}
	send(w, 201, map[string]any{"id": id, "code": gapCode(id, in.Level, in.Category), "status": "pending"})
}
func (s *server) activity(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	ledger := []map[string]any{}
	rows, e := s.db.Query("SELECT kind,amount,created_at,note,expires_at FROM ledger WHERE user_id=? ORDER BY id DESC LIMIT 100", u.ID)
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	for rows.Next() {
		var kind, created, note, expires string
		var amount int
		if rows.Scan(&kind, &amount, &created, &note, &expires) == nil {
			ledger = append(ledger, map[string]any{"kind": kind, "amount": amount, "created_at": created, "note": note, "expires_at": expires})
		}
	}
	rows.Close()
	gaps := []*Gap{}
	rows, e = s.db.Query(gapSelect+"WHERE g.author_id=? ORDER BY g.id DESC LIMIT 100", u.ID, u.ID, u.ID)
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	for rows.Next() {
		g, e := gapScan(rows, u)
		if e == nil {
			gaps = append(gaps, g)
		}
	}
	rows.Close()
	send(w, 200, map[string]any{"ledger": ledger, "submissions": gaps})
}
func audit(tx *sql.Tx, ctx context.Context, u int64, action, obj, note, at string) error {
	_, e := tx.ExecContext(ctx, "INSERT INTO audit(user_id,action,object_id,note,created_at) VALUES(?,?,?,?,?)", u, action, obj, note, at)
	return e
}
