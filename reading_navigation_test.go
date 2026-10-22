package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestReadingNeighborsThemeBoundaryAndNoCharge(t *testing.T) {
	s := testServer(t)
	uid, c := account(t, s, "navigation@example.org", "member", 10)
	a := gap(t, s, "ordinary", "published", nil)
	b := gap(t, s, "ordinary", "published", nil)
	other := gap(t, s, "ordinary", "published", nil)
	s.db.Exec("UPDATE gaps SET category='化学' WHERE id=?", other)
	s.db.Exec("UPDATE gaps SET released_at=? WHERE id=?", stamp(s.now().Add(-time.Hour)), b)
	var out struct {
		Next       *readingNeighbor `json:"next"`
		Transition string           `json:"next_transition"`
	}
	w := request(s, "GET", fmt.Sprintf("/api/gaps/%d/neighbors", b), nil, c)
	mustStatus(t, w, 200)
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Next == nil || out.Next.ID != a || out.Transition != "same" {
		t.Fatal(w.Body.String())
	}
	w = request(s, "GET", fmt.Sprintf("/api/gaps/%d/neighbors?visited=%d", a, b), nil, c)
	mustStatus(t, w, 200)
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Next == nil || out.Next.ID != other || out.Transition != "random" {
		t.Fatal(w.Body.String())
	}
	var credits, n int
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", uid).Scan(&credits)
	s.db.QueryRow("SELECT COUNT(*) FROM unlocks WHERE user_id=?", uid).Scan(&n)
	if credits != 10 || n != 0 {
		t.Fatal("neighbors charged")
	}
	request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", other), nil, c)
	w = request(s, "GET", fmt.Sprintf("/api/gaps/%d/neighbors?visited=%d", a, b), nil, c)
	mustStatus(t, w, 200)
	out.Next = nil
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Next != nil || out.Transition != "end" {
		t.Fatal(w.Body.String())
	}
}
