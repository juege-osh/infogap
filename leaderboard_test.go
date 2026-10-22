package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestLeaderboardContributionRankAndPrivacy(t *testing.T) {
	s := testServer(t)
	id, cookie := account(t, s, "reader@example.com", "member", 0)
	other, _ := account(t, s, "contributor@example.com", "member", 0)
	admin, _ := account(t, s, founderEmail, "admin", 0)
	test, _ := account(t, s, "preview@example.com", "member", 0)
	s.db.Exec("UPDATE users SET points=300 WHERE id=?", id)
	s.db.Exec("UPDATE users SET points=700 WHERE id=?", other)
	s.db.Exec("UPDATE users SET points=100000 WHERE id IN (?,?)", admin, test)
	s.db.Exec("INSERT INTO test_accounts(owner_id,user_id) VALUES(?,?)", admin, test)
	s.db.Exec("UPDATE users SET paid_tier=7,paid_until='2099-01-01T00:00:00Z' WHERE id=?", id)
	w := request(s, "GET", "/api/leaderboard", nil, cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got struct {
		Items []leaderboardEntry `json:"items"`
		Total int                `json:"total"`
		Self  *leaderboardEntry  `json:"self"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || got.Items[0].ID != other || got.Self == nil || got.Self.Rank != 2 || got.Self.TierIndex != 2 {
		t.Fatalf("unexpected: %+v", got)
	}
	if contains := string(w.Body.Bytes()); len(contains) > 0 {
		for _, secret := range []string{"reader@example.com", "paid_until", "credits"} {
			if stringsContains(contains, secret) {
				t.Fatal("private field leaked", secret)
			}
		}
	}
	w = request(s, "GET", "/api/leaderboard?tier=2", nil, cookie)
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Total != 1 || got.Items[0].ID != id {
		t.Fatal(w.Body.String())
	}
	for _, tier := range []string{"-1", "10", "oops"} {
		w = request(s, "GET", "/api/leaderboard?tier="+tier, nil, cookie)
		if w.Code != 400 {
			t.Fatal(tier, w.Code)
		}
	}
	if w = request(s, "GET", "/api/leaderboard", nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for i := 0; i < 22; i++ {
		account(t, s, fmt.Sprintf("extra%d@example.com", i), "member", 0)
	}
	w = request(s, "GET", "/api/leaderboard?page=2", nil, cookie)
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Items) != 4 || got.Items[0].Rank != 21 {
		t.Fatal(w.Body.String())
	}
}
func stringsContains(s, v string) bool {
	for i := 0; i+len(v) <= len(s); i++ {
		if s[i:i+len(v)] == v {
			return true
		}
	}
	return false
}

func TestLeaderboardTiesUseRegistrationTime(t *testing.T) {
	s := testServer(t)
	first, cookie := account(t, s, "first@example.test", "member", 0)
	second, _ := account(t, s, "second@example.test", "member", 0)
	s.db.Exec("UPDATE users SET points=300,contributions=100,created_at='2026-10-22T00:00:00Z' WHERE id=?", first)
	s.db.Exec("UPDATE users SET points=300,created_at='2026-10-21T00:00:00Z' WHERE id=?", second)
	w := request(s, "GET", "/api/leaderboard?tier=2", nil, cookie)
	var data struct {
		Items []leaderboardEntry `json:"items"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &data); e != nil {
		t.Fatal(e)
	}
	if len(data.Items) != 2 || data.Items[0].ID != second {
		t.Fatal(w.Body.String())
	}
}
