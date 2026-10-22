package main

import (
	"database/sql"
	"net/http"
	"strings"
	"unicode/utf8"
)

func migrateSchema(db *sql.DB) error {
	if err := ensureColumn(db, "invites", "permanent", "INTEGER NOT NULL DEFAULT 0 CHECK(permanent IN (0,1))"); err != nil {
		return err
	}
	if err := ensureColumn(db, "contribution_rewards", "credits", "INTEGER NOT NULL DEFAULT 100"); err != nil {
		return err
	}
	if err := migratePaidRanks(db); err != nil {
		return err
	}
	if err := migrateMembership(db); err != nil {
		return err
	}
	if err := migrateRelease(db); err != nil {
		return err
	}
	if err := migrateReadingCredits(db); err != nil {
		return err
	}
	return migrateSourceVisibility(db)
}

func migrateMembership(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(users)")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "membership_until" {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		_, err = db.Exec("ALTER TABLE users ADD COLUMN membership_until TEXT NOT NULL DEFAULT ''")
	}
	return err
}

type Subscription struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	CreatedAt string `json:"created_at"`
	Active    bool   `json:"active"`
}

func (s *server) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	rows, e := s.db.Query("SELECT id,kind,value,created_at FROM subscriptions WHERE user_id=? ORDER BY id", u.ID)
	if e != nil {
		fail(w, 500, "读取订阅失败")
		return
	}
	defer rows.Close()
	items := []Subscription{}
	for rows.Next() {
		var sub Subscription
		if e = rows.Scan(&sub.ID, &sub.Kind, &sub.Value, &sub.CreatedAt); e != nil {
			fail(w, 500, "读取订阅失败")
			return
		}
		sub.Active = len(items) < u.Entitlements.SubscriptionLimit
		items = append(items, sub)
	}
	send(w, 200, map[string]any{"items": items, "limit": u.Entitlements.SubscriptionLimit, "enabled": u.Entitlements.Subscriptions})
}
func (s *server) addSubscription(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	var in struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Value = strings.TrimSpace(in.Value)
	if !has([]string{"category", "tag"}, in.Kind) || utf8.RuneCountInString(in.Value) < 1 || utf8.RuneCountInString(in.Value) > 24 || strings.ContainsAny(in.Value, "\r\n\t<>\x00") {
		fail(w, 400, "请选择领域或填写1–24字标签")
		return
	}
	if in.Kind == "category" && !has(categories, in.Value) {
		fail(w, 400, "请选择已有学科领域")
		return
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, "添加订阅失败")
		return
	}
	defer tx.Rollback()
	u, e = userScan(tx.QueryRow("SELECT "+userCols+" FROM users WHERE id=? AND disabled=0", u.ID))
	if e != nil {
		fail(w, 401, "请重新登录")
		return
	}
	s.decorate(u)
	if !u.Entitlements.Subscriptions {
		fail(w, 403, "订阅功能需白银及以上有效段位")
		return
	}
	var sub Subscription
	e = tx.QueryRow("SELECT id,kind,value,created_at FROM subscriptions WHERE user_id=? AND kind=? AND value=?", u.ID, in.Kind, in.Value).Scan(&sub.ID, &sub.Kind, &sub.Value, &sub.CreatedAt)
	if e == nil {
		var prior int
		_ = tx.QueryRow("SELECT COUNT(*) FROM subscriptions WHERE user_id=? AND id<?", u.ID, sub.ID).Scan(&prior)
		sub.Active = prior < u.Entitlements.SubscriptionLimit
		send(w, 200, sub)
		return
	}
	if e != sql.ErrNoRows {
		fail(w, 500, "添加订阅失败")
		return
	}
	var count int
	if e = tx.QueryRow("SELECT COUNT(*) FROM subscriptions WHERE user_id=?", u.ID).Scan(&count); e != nil {
		fail(w, 500, "添加订阅失败")
		return
	}
	if count >= u.Entitlements.SubscriptionLimit {
		fail(w, 409, "已达到订阅数量上限，请先删除不需要的订阅")
		return
	}
	sub = Subscription{Kind: in.Kind, Value: in.Value, CreatedAt: stamp(s.now()), Active: true}
	res, e := tx.Exec("INSERT INTO subscriptions(user_id,kind,value,created_at) VALUES(?,?,?,?)", u.ID, sub.Kind, sub.Value, sub.CreatedAt)
	if e == nil {
		sub.ID, e = res.LastInsertId()
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "添加订阅失败")
		return
	}
	send(w, 201, sub)
}
func (s *server) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, "删除订阅失败")
		return
	}
	defer tx.Rollback()
	u, e = userScan(tx.QueryRow("SELECT "+userCols+" FROM users WHERE id=? AND disabled=0", u.ID))
	if e != nil {
		fail(w, 401, "请重新登录")
		return
	}
	s.decorate(u)
	if !u.Entitlements.Subscriptions {
		fail(w, 403, "订阅功能需白银及以上有效段位")
		return
	}
	res, e := tx.Exec("DELETE FROM subscriptions WHERE id=? AND user_id=?", idParam(r), u.ID)
	if e != nil {
		fail(w, 500, "删除订阅失败")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "订阅不存在")
		return
	}
	if tx.Commit() != nil {
		fail(w, 500, "删除订阅失败")
		return
	}
	send(w, 200, map[string]bool{"ok": true})
}

func migrateRelease(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(gaps)")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "released_at" {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		if _, err = db.Exec("ALTER TABLE gaps ADD COLUMN released_at TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		_, err = db.Exec("UPDATE gaps SET released_at=created_at WHERE status IN ('published','expired','disputed') AND released_at=''")
	}
	return err
}
