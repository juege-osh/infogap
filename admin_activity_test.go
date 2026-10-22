package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestBehaviorDailyCompactionAndAdminPrivacy(t *testing.T) {
	s := testServer(t)
	uid, cookie := account(t, s, "behavior@example.org", "member", 50)
	_, admin := account(t, s, founderEmail, "admin", 0)
	mustStatus(t, request(s, "GET", "/api/admin/behaviors", nil, cookie), 403)
	mustStatus(t, request(s, "GET", "/api/gaps", nil, cookie), 200)
	// A failed public action is recorded, but payloads and query strings are not.
	request(s, "GET", "/api/gaps?q=private-query", nil, cookie)
	now := s.now()
	s.now = func() time.Time { return now.Add(24 * time.Hour) }
	s.db.Exec("UPDATE sessions SET expires_at=?", stamp(s.now().Add(time.Hour)))
	if e := s.compactBehaviors(); e != nil {
		t.Fatal(e)
	}
	if e := s.compactBehaviors(); e != nil {
		t.Fatal(e)
	}
	var count, events int
	if e := s.db.QueryRow("SELECT COUNT(*),SUM(event_count) FROM behavior_days WHERE user_id=?", uid).Scan(&count, &events); e != nil {
		t.Fatal(e)
	}
	if count != 1 || events != 2 {
		t.Fatalf("count=%d events=%d", count, events)
	}
	var raw int
	s.db.QueryRow("SELECT COUNT(*) FROM user_behaviors WHERE user_id=?", uid).Scan(&raw)
	if raw != 0 {
		t.Fatal(raw)
	}
	w := request(s, "GET", fmt.Sprintf("/api/admin/behaviors?user_id=%d", uid), nil, admin)
	mustStatus(t, w, 200)
	var data struct {
		Items []struct {
			IsDaily    bool `json:"is_daily"`
			EventCount int  `json:"event_count"`
		}
	}
	if e := json.Unmarshal(w.Body.Bytes(), &data); e != nil {
		t.Fatal(e)
	}
	if len(data.Items) != 1 || !data.Items[0].IsDaily || data.Items[0].EventCount != 2 {
		t.Fatal(w.Body.String())
	}
	var empty int
	s.db.QueryRow("SELECT COUNT(*) FROM behavior_days WHERE user_id!=?", uid).Scan(&empty)
	if empty != 0 {
		t.Fatal("empty/admin days generated")
	}
}
func TestAdminOrderAccessAndFilters(t *testing.T) {
	s := testServer(t)
	_, m := account(t, s, "orders@example.org", "member", 0)
	_, admin := account(t, s, founderEmail, "admin", 0)
	mustStatus(t, request(s, "GET", "/api/admin/orders", nil, m), 403)
	mustStatus(t, request(s, "GET", "/api/admin/orders?status=bogus", nil, admin), 400)
	w := request(s, "GET", "/api/admin/orders?status=success&q=missing", nil, admin)
	mustStatus(t, w, 200)
}
