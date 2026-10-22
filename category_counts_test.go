package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCategoryCountsRespectPublicationAndDelay(t *testing.T) {
	s := testServer(t)
	_, cookie := account(t, s, "member@example.test", "member", 0)
	visible := gap(t, s, "ordinary", "published", nil)
	gap(t, s, "ordinary", "pending", nil)
	recent := gap(t, s, "ordinary", "published", nil)
	s.db.Exec("UPDATE gaps SET released_at=? WHERE id=?", stamp(s.now().Add(-96*time.Hour)), visible)
	s.db.Exec("UPDATE gaps SET released_at=? WHERE id=?", stamp(s.now()), recent)
	w := request(s, "GET", "/api/bootstrap", nil, cookie)
	var data struct {
		Counts map[string]int `json:"category_counts"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &data); e != nil {
		t.Fatal(e)
	}
	if data.Counts["物理"] != 1 || data.Counts["招聘求职"] != 0 {
		t.Fatal(data.Counts)
	}
	_, admin := account(t, s, founderEmail, "admin", 0)
	w = request(s, "GET", "/api/bootstrap", nil, admin)
	json.Unmarshal(w.Body.Bytes(), &data)
	if data.Counts["物理"] != 2 {
		t.Fatal(data.Counts)
	}
}
