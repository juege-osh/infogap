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

func initFeedback(db *sql.DB) error {
	_, err := db.Exec(`
 CREATE TABLE IF NOT EXISTS feedback(
 id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER NOT NULL REFERENCES users(id),
 title TEXT NOT NULL,body TEXT NOT NULL,type TEXT NOT NULL CHECK(type IN ('idea','problem')),
 status TEXT NOT NULL DEFAULT 'new' CHECK(status IN ('new','reviewing','planned','working','resolved','declined')),
 reward_credits INTEGER NOT NULL DEFAULT 0 CHECK(reward_credits>=0),created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS feedback_user_time ON feedback(user_id,updated_at DESC,id DESC);
 CREATE INDEX IF NOT EXISTS feedback_status_time ON feedback(status,updated_at DESC,id DESC);
 CREATE TABLE IF NOT EXISTS feedback_replies(
 id INTEGER PRIMARY KEY AUTOINCREMENT,feedback_id INTEGER NOT NULL REFERENCES feedback(id),
 user_id INTEGER NOT NULL REFERENCES users(id),body TEXT NOT NULL,created_at TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS feedback_reply_order ON feedback_replies(feedback_id,id);
 CREATE TABLE IF NOT EXISTS feedback_rewards(
 feedback_id INTEGER PRIMARY KEY REFERENCES feedback(id),user_id INTEGER NOT NULL REFERENCES users(id),
 credits INTEGER NOT NULL CHECK(credits BETWEEN 1 AND 10000),ledger_id INTEGER NOT NULL UNIQUE REFERENCES ledger(id),
 created_by INTEGER NOT NULL REFERENCES users(id),created_at TEXT NOT NULL);
 `)
	return err
}
func (s *server) feedbackRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/feedback", s.listFeedback)
	m.HandleFunc("POST /api/feedback", s.createFeedback)
	m.HandleFunc("GET /api/feedback/{id}", s.getFeedback)
	m.HandleFunc("POST /api/feedback/{id}/replies", s.replyFeedback)
	m.HandleFunc("POST /api/admin/feedback/{id}", s.updateFeedback)
	m.HandleFunc("POST /api/admin/feedback/{id}/reward", s.rewardFeedback)
}

type feedbackItem struct {
	ID            int64  `json:"id"`
	UserID        int64  `json:"user_id"`
	Username      string `json:"username"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	Type          string `json:"type"`
	Status        string `json:"status"`
	RewardCredits int    `json:"reward_credits"`
	ReplyCount    int    `json:"reply_count"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}
type feedbackReply struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	IsAdmin   bool   `json:"is_admin"`
	Username  string `json:"username"`
	CreatedAt string `json:"created_at"`
}

const feedbackSelect = `SELECT f.id,f.user_id,u.username,f.title,f.body,f.type,f.status,f.reward_credits,(SELECT COUNT(*) FROM feedback_replies fr WHERE fr.feedback_id=f.id),f.created_at,f.updated_at FROM feedback f JOIN users u ON u.id=f.user_id `

func scanFeedback(row scanner) (*feedbackItem, error) {
	f := &feedbackItem{}
	err := row.Scan(&f.ID, &f.UserID, &f.Username, &f.Title, &f.Body, &f.Type, &f.Status, &f.RewardCredits, &f.ReplyCount, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}
func validFeedbackStatus(status string) bool {
	return has([]string{"new", "reviewing", "planned", "working", "resolved", "declined"}, status)
}
func feedbackScope(u *User) (string, []any) {
	if u.Role == "admin" {
		return "", nil
	}
	return " AND f.user_id=?", []any{u.ID}
}
func (s *server) feedbackUser(w http.ResponseWriter, r *http.Request, write bool) *User {
	u := s.require(w, r)
	if u == nil {
		return nil
	}
	if write && u.TestMode {
		fail(w, 403, "请先退出测试模式，再提交正式意见或回复")
		return nil
	}
	return u
}
func (s *server) feedbackByID(id int64, u *User) (*feedbackItem, error) {
	scope, args := feedbackScope(u)
	args = append([]any{id}, args...)
	return scanFeedback(s.db.QueryRow(feedbackSelect+"WHERE f.id=?"+scope, args...))
}
func (s *server) listFeedback(w http.ResponseWriter, r *http.Request) {
	u := s.feedbackUser(w, r, false)
	if u == nil {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && !validFeedbackStatus(status) {
		fail(w, 400, "意见状态不正确")
		return
	}
	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 100000 {
			fail(w, 400, "页码不正确")
			return
		}
		page = n
	}
	where, args := feedbackScope(u)
	where = " WHERE 1=1" + where
	if status != "" {
		where += " AND f.status=?"
		args = append(args, status)
	}
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM feedback f"+where, args...).Scan(&total); err != nil {
		fail(w, 500, "读取意见列表失败")
		return
	}
	pages := (total + 19) / 20
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	queryArgs := append(append([]any{}, args...), 20, (page-1)*20)
	rows, err := s.db.Query(feedbackSelect+where+" ORDER BY f.updated_at DESC,f.id DESC LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		fail(w, 500, "读取意见列表失败")
		return
	}
	defer rows.Close()
	items := []*feedbackItem{}
	for rows.Next() {
		item, e := scanFeedback(rows)
		if e != nil {
			fail(w, 500, "读取意见失败")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		fail(w, 500, "读取意见失败")
		return
	}
	send(w, 200, map[string]any{"items": items, "page": page, "pages": pages, "total": total, "can_manage": u.Role == "admin"})
}
func (s *server) createFeedback(w http.ResponseWriter, r *http.Request) {
	u := s.feedbackUser(w, r, true)
	if u == nil {
		return
	}
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Type  string `json:"type"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Body = strings.TrimSpace(in.Body)
	if n := utf8.RuneCountInString(in.Title); n < 2 || n > 120 {
		fail(w, 400, "标题需2～120字")
		return
	}
	if n := utf8.RuneCountInString(in.Body); n < 10 || n > 3000 {
		fail(w, 400, "内容需10～3000字")
		return
	}
	if !has([]string{"idea", "problem"}, in.Type) {
		fail(w, 400, "请选择意见建议或问题反馈")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "意见暂时无法保存")
		return
	}
	defer tx.Rollback()
	var recent int
	if err = tx.QueryRow("SELECT COUNT(*) FROM feedback WHERE user_id=? AND created_at>?", u.ID, stamp(s.now().Add(-24*time.Hour))).Scan(&recent); err != nil {
		fail(w, 500, "读取提交记录失败")
		return
	}
	if recent >= 10 {
		fail(w, 429, "每天最多提交10条意见，请等待处理后再来")
		return
	}
	at := stamp(s.now())
	result, err := tx.Exec("INSERT INTO feedback(user_id,title,body,type,created_at,updated_at) VALUES(?,?,?,?,?,?)", u.ID, in.Title, in.Body, in.Type, at, at)
	if err != nil {
		fail(w, 500, "意见未保存，请稍后重试")
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		fail(w, 500, "意见未保存")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "意见未保存")
		return
	}
	item, err := s.feedbackByID(id, u)
	if err != nil {
		fail(w, 500, "意见已保存，请刷新列表查看")
		return
	}
	send(w, 201, map[string]any{"feedback": item})
}
func (s *server) getFeedback(w http.ResponseWriter, r *http.Request) {
	u := s.feedbackUser(w, r, false)
	if u == nil {
		return
	}
	item, err := s.feedbackByID(idParam(r), u)
	if err != nil {
		fail(w, 404, "意见不存在或无权查看")
		return
	}
	rows, err := s.db.Query("SELECT fr.id,fr.body,u.role='admin',u.username,fr.created_at FROM feedback_replies fr JOIN users u ON u.id=fr.user_id WHERE fr.feedback_id=? ORDER BY fr.id", item.ID)
	if err != nil {
		fail(w, 500, "读取回复失败")
		return
	}
	defer rows.Close()
	replies := []feedbackReply{}
	for rows.Next() {
		var reply feedbackReply
		if err = rows.Scan(&reply.ID, &reply.Body, &reply.IsAdmin, &reply.Username, &reply.CreatedAt); err != nil {
			fail(w, 500, "读取回复失败")
			return
		}
		replies = append(replies, reply)
	}
	if rows.Err() != nil {
		fail(w, 500, "读取回复失败")
		return
	}
	send(w, 200, map[string]any{"feedback": item, "replies": replies})
}
func (s *server) replyFeedback(w http.ResponseWriter, r *http.Request) {
	u := s.feedbackUser(w, r, true)
	if u == nil {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	if n := utf8.RuneCountInString(in.Body); n < 1 || n > 3000 {
		fail(w, 400, "回复需1～3000字")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "回复暂时无法保存")
		return
	}
	defer tx.Rollback()
	scope, args := feedbackScope(u)
	args = append([]any{idParam(r)}, args...)
	var id int64
	if err = tx.QueryRow("SELECT f.id FROM feedback f WHERE f.id=?"+scope, args...).Scan(&id); err != nil {
		fail(w, 404, "意见不存在或无权回复")
		return
	}
	at := stamp(s.now())
	result, err := tx.Exec("INSERT INTO feedback_replies(feedback_id,user_id,body,created_at) VALUES(?,?,?,?)", id, u.ID, in.Body, at)
	if err != nil {
		fail(w, 500, "回复未保存")
		return
	}
	replyID, err := result.LastInsertId()
	if err != nil {
		fail(w, 500, "回复未保存")
		return
	}
	if _, err = tx.Exec("UPDATE feedback SET updated_at=? WHERE id=?", at, id); err != nil {
		fail(w, 500, "回复未保存")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "回复未保存")
		return
	}
	send(w, 201, map[string]any{"reply": feedbackReply{ID: replyID, Body: in.Body, IsAdmin: u.Role == "admin", Username: u.Username, CreatedAt: at}})
}
func (s *server) updateFeedback(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, true)
	if u == nil {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validFeedbackStatus(in.Status) {
		fail(w, 400, "意见状态不正确")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "更新状态失败")
		return
	}
	defer tx.Rollback()
	var previous string
	if err = tx.QueryRow("SELECT status FROM feedback WHERE id=?", idParam(r)).Scan(&previous); err != nil {
		fail(w, 404, "意见不存在")
		return
	}
	at := stamp(s.now())
	if _, err = tx.Exec("UPDATE feedback SET status=?,updated_at=? WHERE id=?", in.Status, at, idParam(r)); err != nil {
		fail(w, 500, "更新状态失败")
		return
	}
	if err = audit(tx, r.Context(), u.ID, "feedback_status", strconv.FormatInt(idParam(r), 10), previous+" -> "+in.Status, at); err != nil {
		fail(w, 500, "更新状态失败")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "更新状态失败")
		return
	}
	item, err := s.feedbackByID(idParam(r), u)
	if err != nil {
		fail(w, 500, "状态已更新，请刷新查看")
		return
	}
	send(w, 200, map[string]any{"feedback": item})
}
func (s *server) rewardFeedback(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, true)
	if u == nil {
		return
	}
	var in struct {
		Credits int `json:"credits"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Credits < 1 || in.Credits > 10000 {
		fail(w, 400, "奖励需为1～10000的整数积分")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "奖励暂时无法发放")
		return
	}
	defer tx.Rollback()
	var userID int64
	var disabled int
	if err = tx.QueryRow("SELECT f.user_id,u.disabled FROM feedback f JOIN users u ON u.id=f.user_id WHERE f.id=?", idParam(r)).Scan(&userID, &disabled); err != nil {
		fail(w, 404, "意见不存在")
		return
	}
	var previous int
	err = tx.QueryRow("SELECT credits FROM feedback_rewards WHERE feedback_id=?", idParam(r)).Scan(&previous)
	if err == nil {
		if previous != in.Credits {
			fail(w, 409, "这条意见已经发过奖励，不能重复或修改奖励金额")
			return
		}
		tx.Rollback()
		item, e := s.feedbackByID(idParam(r), u)
		if e != nil {
			fail(w, 500, "读取奖励记录失败")
			return
		}
		send(w, 200, map[string]any{"feedback": item, "awarded_credits": previous, "already_awarded": true})
		return
	}
	if err != sql.ErrNoRows {
		fail(w, 500, "读取奖励记录失败")
		return
	}
	if disabled != 0 {
		fail(w, 409, "该成员已停用，请确认账号状态后再发放奖励")
		return
	}
	at := stamp(s.now())
	result, err := tx.Exec("INSERT INTO ledger(user_id,kind,amount,created_at,note) VALUES(?,'feedback_reward',?,?,?)", userID, in.Credits, at, "意见反馈奖励 #"+strconv.FormatInt(idParam(r), 10))
	if err != nil {
		fail(w, 500, "奖励未发放")
		return
	}
	ledgerID, err := result.LastInsertId()
	if err != nil {
		fail(w, 500, "奖励未发放")
		return
	}
	if _, err = tx.Exec("INSERT INTO feedback_rewards(feedback_id,user_id,credits,ledger_id,created_by,created_at) VALUES(?,?,?,?,?,?)", idParam(r), userID, in.Credits, ledgerID, u.ID, at); err != nil {
		fail(w, 409, "奖励记录发生冲突，请刷新后核对")
		return
	}
	if _, err = tx.Exec("UPDATE users SET credits=credits+? WHERE id=?", in.Credits, userID); err != nil {
		fail(w, 500, "奖励未发放")
		return
	}
	if _, err = tx.Exec("UPDATE feedback SET reward_credits=?,updated_at=? WHERE id=?", in.Credits, at, idParam(r)); err != nil {
		fail(w, 500, "奖励未发放")
		return
	}
	note, _ := json.Marshal(map[string]int{"credits": in.Credits})
	if err = audit(tx, r.Context(), u.ID, "feedback_reward", strconv.FormatInt(idParam(r), 10), string(note), at); err != nil {
		fail(w, 500, "奖励未发放")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "奖励未发放，请刷新核对后重试")
		return
	}
	item, err := s.feedbackByID(idParam(r), u)
	if err != nil {
		fail(w, 500, "奖励已发放，请刷新查看")
		return
	}
	send(w, 200, map[string]any{"feedback": item, "awarded_credits": in.Credits, "already_awarded": false})
}
