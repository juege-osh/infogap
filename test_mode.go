package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"
)

// Authorization always uses the original session, never a client-provided user ID.
func (s *server) testModeOwner(w http.ResponseWriter, r *http.Request) *User {
	u := s.actor(r)
	if u == nil {
		fail(w, 401, "请先登录")
		return nil
	}
	if !u.CanTestMode {
		fail(w, 403, "仅站长可以切换测试模式")
		return nil
	}
	return u
}

func (s *server) setTestMode(w http.ResponseWriter, r *http.Request) {
	owner := s.testModeOwner(w, r)
	if owner == nil {
		return
	}
	var in struct {
		ReadingMonths *int `json:"reading_months"`
		Points        *int `json:"points"`
		Tier          *int `json:"tier"`
		Credits       int  `json:"credits"`
		Member        bool `json:"member"`
		Quality       bool `json:"quality"`
		Reset         bool `json:"reset"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Tier == nil {
		zero := 0
		in.Tier = &zero
	}
	if in.ReadingMonths != nil && !hasInt([]int{0, 1, 3, 6, 12}, *in.ReadingMonths) {
		fail(w, 400, "套餐无效")
		return
	}
	if in.Points != nil && (*in.Points < 0 || *in.Points > 1000000) {
		fail(w, 400, "贡献值无效")
		return
	}
	if *in.Tier < 0 || *in.Tier >= len(tiers) || in.Credits < 0 || in.Credits > 100000 {
		fail(w, 400, "请选择有效段位，测试额度范围为 0–100000")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "切换失败")
		return
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow("SELECT user_id FROM test_accounts WHERE owner_id=?", owner.ID).Scan(&id)
	if err == sql.ErrNoRows {
		result, e := tx.Exec("INSERT INTO users(email,username,role,credits,created_at) VALUES(?,?,'member',0,?)", fmt.Sprintf("preview-%d@example.invalid", owner.ID), "站长测试账号-"+token(4), stamp(s.now()))
		if e != nil {
			fail(w, 500, "创建测试账号失败")
			return
		}
		id, err = result.LastInsertId()
		if err == nil {
			_, err = tx.Exec("INSERT INTO test_accounts(owner_id,user_id) VALUES(?,?)", owner.ID, id)
		}
	}
	if err != nil {
		fail(w, 500, "读取测试账号失败")
		return
	}
	points := tiers[*in.Tier].MinPoints
	if in.Points != nil {
		points = *in.Points
	}
	until, quality := "", ""
	contributions, featured, rare := 0, 0, 0
	if in.Member {
		until = stamp(s.now().Add(30 * 24 * time.Hour))
	}
	if in.Quality {
		contributions, featured, rare = 10, 5, 1
		quality = stamp(s.now())
	}
	_, err = tx.Exec("UPDATE users SET points=?,credits=?,membership_until=?,contributions=?,featured_contributions=?,rare_contributions=?,last_quality_at=?,day_key=?,daily_used=0 WHERE id=? AND role='member'", points, in.Credits, until, contributions, featured, rare, quality, dayKey(s.now()), id)
	if err == nil && in.Reset {
		for _, table := range []string{"unlocks", "saves", "subscriptions", "ledger"} {
			if _, err = tx.Exec("DELETE FROM "+table+" WHERE user_id=?", id); err != nil {
				break
			}
		}
	}
	if err == nil && in.ReadingMonths != nil {
		err = setTestReading(tx, id, *in.ReadingMonths, s.now())
	}
	cookie, _ := r.Cookie("infogap_session")
	if err == nil {
		_, err = tx.Exec("INSERT INTO test_sessions(session_hash,user_id) VALUES(?,?) ON CONFLICT(session_hash) DO UPDATE SET user_id=excluded.user_id", digest(cookie.Value), id)
	}
	if err == nil {
		_, err = tx.Exec("INSERT INTO audit(user_id,action,object_id,note,created_at) VALUES(?,'test_mode',?,'切换测试用户段位与权益',?)", owner.ID, fmt.Sprint(id), stamp(s.now()))
	}
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "切换测试模式失败")
		return
	}
	send(w, 200, map[string]any{"user": s.current(r)})
}

func (s *server) exitTestMode(w http.ResponseWriter, r *http.Request) {
	owner := s.testModeOwner(w, r)
	if owner == nil {
		return
	}
	cookie, _ := r.Cookie("infogap_session")
	if _, err := s.db.Exec("DELETE FROM test_sessions WHERE session_hash=?", digest(cookie.Value)); err != nil {
		fail(w, 500, "返回管理员模式失败")
		return
	}
	send(w, 200, map[string]any{"user": owner})
}
