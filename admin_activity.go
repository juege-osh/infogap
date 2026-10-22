package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const behaviorSchema = `
CREATE TABLE IF NOT EXISTS user_behaviors(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER NOT NULL REFERENCES users(id),day TEXT NOT NULL,action TEXT NOT NULL,object_id TEXT NOT NULL DEFAULT '',status INTEGER NOT NULL,created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS behavior_user_day ON user_behaviors(user_id,day,id);
CREATE TABLE IF NOT EXISTS behavior_days(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER NOT NULL REFERENCES users(id),day TEXT NOT NULL,event_count INTEGER NOT NULL,events TEXT NOT NULL,updated_at TEXT NOT NULL,UNIQUE(user_id,day));`

type behaviorEvent struct {
	Action    string `json:"action"`
	ObjectID  string `json:"object_id"`
	CreatedAt string `json:"created_at"`
	Status    int    `json:"status"`
}

// Request bodies, tokens, query contents and IP addresses are deliberately not recorded.
func behaviorAction(r *http.Request) (string, string) {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/admin/") || strings.HasPrefix(p, "/api/billing/notify/") {
		return "", ""
	}
	if r.Method == "GET" {
		switch {
		case strings.HasPrefix(p, "/api/courses/") && strings.HasSuffix(p, "/video"):
			return "观看课程视频", r.PathValue("id")
		case strings.HasPrefix(p, "/api/courses"):
			return "浏览课程", r.PathValue("id")
		case p == "/api/bootstrap":
			return "访问网站", ""
		case p == "/api/gaps":
			return "浏览情报列表", ""
		case strings.HasPrefix(p, "/api/gaps/"):
			return "查看情报", r.PathValue("id")
		case p == "/api/me/submissions":
			return "查看我的发布", ""
		case p == "/api/billing/orders":
			return "查看订单", ""
		}
	} else {
		switch {
		case p == "/api/auth/verify":
			return "登录或注册", ""
		case strings.HasSuffix(p, "/unlock"):
			return "解锁情报", r.PathValue("id")
		case strings.HasSuffix(p, "/save"):
			return "收藏操作", r.PathValue("id")
		case p == "/api/submissions":
			return "提交情报", ""
		case strings.HasPrefix(p, "/api/submissions/"):
			return "修改投稿", r.PathValue("id")
		case p == "/api/billing/orders":
			return "创建订单", ""
		case p == "/api/feedback":
			return "提交意见", ""
		case p == "/api/subscriptions":
			return "订阅操作", ""
		}
	}
	return "", ""
}
func (s *server) recordBehavior(user int64, r *http.Request, status int) {
	if user <= 0 {
		return
	}
	action, obj := behaviorAction(r)
	if action == "" {
		return
	}
	_, err := s.db.Exec("INSERT INTO user_behaviors(user_id,day,action,object_id,status,created_at) VALUES(?,?,?,?,?,?)", user, dayKey(s.now()), action, obj, status, stamp(s.now()))
	if err != nil {
		return
	}
}
func (s *server) compactBehaviors() error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	rows, e := tx.Query("SELECT id,user_id,day,action,object_id,status,created_at FROM user_behaviors WHERE day<? ORDER BY user_id,day,id", dayKey(s.now()))
	if e != nil {
		return e
	}
	type group struct {
		user   int64
		day    string
		events []behaviorEvent
		ids    []int64
	}
	groups := map[string]*group{}
	for rows.Next() {
		var id, user int64
		var day string
		var v behaviorEvent
		if e = rows.Scan(&id, &user, &day, &v.Action, &v.ObjectID, &v.Status, &v.CreatedAt); e != nil {
			rows.Close()
			return e
		}
		key := fmt.Sprintf("%d/%s", user, day)
		g := groups[key]
		if g == nil {
			g = &group{user: user, day: day}
			groups[key] = g
		}
		g.events = append(g.events, v)
		g.ids = append(g.ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, g := range groups {
		var old string
		var all []behaviorEvent
		e = tx.QueryRow("SELECT events FROM behavior_days WHERE user_id=? AND day=?", g.user, g.day).Scan(&old)
		if e == nil {
			if e = json.Unmarshal([]byte(old), &all); e != nil {
				return e
			}
		} else if e != sql.ErrNoRows {
			return e
		}
		all = append(all, g.events...)
		b, _ := json.Marshal(all)
		if _, e = tx.Exec("INSERT INTO behavior_days(user_id,day,event_count,events,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(user_id,day) DO UPDATE SET event_count=excluded.event_count,events=excluded.events,updated_at=excluded.updated_at", g.user, g.day, len(all), string(b), stamp(s.now())); e != nil {
			return e
		}
		if _, e = tx.Exec("DELETE FROM user_behaviors WHERE user_id=? AND day=?", g.user, g.day); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *server) runBehaviorCompaction(ctx context.Context) {
	_ = s.compactBehaviors()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.compactBehaviors()
		}
	}
}
func adminPage(r *http.Request) (int, int) {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	if p > 100000 {
		p = 100000
	}
	return p, (p - 1) * 20
}
func (s *server) adminBehaviors(w http.ResponseWriter, r *http.Request) {
	if s.staff(w, r, true) == nil {
		return
	}
	if e := s.compactBehaviors(); e != nil {
		fail(w, 500, "行为记录整理失败")
		return
	}
	where := " WHERE 1=1"
	args := []any{}
	if v := r.URL.Query().Get("user_id"); v != "" {
		id, e := strconv.ParseInt(v, 10, 64)
		if e != nil || id < 1 {
			fail(w, 400, "用户编号无效")
			return
		}
		where += " AND b.user_id=?"
		args = append(args, id)
	}
	if v := r.URL.Query().Get("date"); v != "" {
		if _, e := time.Parse("2006-01-02", v); e != nil {
			fail(w, 400, "日期无效")
			return
		}
		where += " AND b.day=?"
		args = append(args, v)
	}
	base := ` FROM (SELECT id,user_id,day,1 AS is_daily,event_count,events,day AS sort_at FROM behavior_days UNION ALL SELECT id,user_id,day,0,1,json_array(json_object('action',action,'object_id',object_id,'created_at',created_at,'status',status)),created_at FROM user_behaviors) b JOIN users u ON u.id=b.user_id`
	var total int
	if e := s.db.QueryRow("SELECT COUNT(*)"+base+where, args...).Scan(&total); e != nil {
		fail(w, 500, "读取失败")
		return
	}
	page, offset := adminPage(r)
	queryargs := append(append([]any{}, args...), 20, offset)
	rows, e := s.db.Query("SELECT b.id,b.user_id,u.username,b.day,b.is_daily,b.event_count,b.events"+base+where+" ORDER BY b.sort_at DESC,b.user_id,b.id DESC LIMIT ? OFFSET ?", queryargs...)
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, user int64
		var name, day, raw string
		var daily, count int
		if rows.Scan(&id, &user, &name, &day, &daily, &count, &raw) != nil {
			fail(w, 500, "读取失败")
			return
		}
		events := []behaviorEvent{}
		if json.Unmarshal([]byte(raw), &events) != nil {
			fail(w, 500, "记录格式异常")
			return
		}
		items = append(items, map[string]any{"id": id, "user_id": user, "username": name, "date": day, "is_daily": daily == 1, "event_count": count, "events": events})
	}
	send(w, 200, map[string]any{"items": items, "total": total, "page": page, "pages": max(1, (total+19)/20)})
}
func (s *server) adminOrders(w http.ResponseWriter, r *http.Request) {
	if s.staff(w, r, true) == nil {
		return
	}
	where := " WHERE 1=1"
	args := []any{}
	if v := r.URL.Query().Get("user_id"); v != "" {
		n, e := strconv.ParseInt(v, 10, 64)
		if e != nil || n < 1 {
			fail(w, 400, "用户编号无效")
			return
		}
		where += " AND o.user_id=?"
		args = append(args, n)
	}
	if v := r.URL.Query().Get("status"); v != "" {
		if !has([]string{"pending", "success", "failed", "expired"}, v) {
			fail(w, 400, "订单状态无效")
			return
		}
		where += " AND o.status=?"
		args = append(args, v)
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		if len(q) > 200 {
			fail(w, 400, "搜索内容过长")
			return
		}
		where += " AND (o.id LIKE ? OR u.username LIKE ? OR u.email LIKE ?)"
		for i := 0; i < 3; i++ {
			args = append(args, "%"+q+"%")
		}
	}
	base := " FROM billing_orders o JOIN users u ON u.id=o.user_id"
	var total int
	if e := s.db.QueryRow("SELECT COUNT(*)"+base+where, args...).Scan(&total); e != nil {
		fail(w, 500, "订单读取失败")
		return
	}
	page, offset := adminPage(r)
	a := append(append([]any{}, args...), 20, offset)
	rows, e := s.db.Query("SELECT o.id,o.user_id,u.username,u.email,o.plan,o.name,o.amount_cents,o.status,o.created_at,o.paid_at,o.platform_trade_no"+base+where+" ORDER BY o.created_at DESC,o.id DESC LIMIT ? OFFSET ?", a...)
	if e != nil {
		fail(w, 500, "订单读取失败")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, email, plan, pname, status, created, paid, trade string
		var user, cents int64
		if rows.Scan(&id, &user, &name, &email, &plan, &pname, &cents, &status, &created, &paid, &trade) != nil {
			fail(w, 500, "订单读取失败")
			return
		}
		items = append(items, map[string]any{"id": id, "user_id": user, "username": name, "email": email, "plan": plan, "name": pname, "amount_cents": cents, "status": status, "created_at": created, "paid_at": paid, "platform_trade_no": trade})
	}
	send(w, 200, map[string]any{"items": items, "total": total, "page": page, "pages": max(1, (total+19)/20)})
}
