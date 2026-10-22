package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseDelayAcrossAllReadPathsAndBoundary(t *testing.T) {
	s := testServer(t)
	id, c := account(t, s, "reader@example.org", "member", 10)
	g := gap(t, s, "ordinary", "published", nil)
	release := s.now().Add(-72*time.Hour + time.Second)
	_, _ = s.db.Exec("UPDATE gaps SET released_at=? WHERE id=?", stamp(release), g)
	_, _ = s.db.Exec("UPDATE users SET membership_until=? WHERE id=?", stamp(s.now().Add(30*24*time.Hour)), id)
	_, _ = s.db.Exec("INSERT INTO saves(user_id,gap_id,created_at) VALUES(?,?,?)", id, g, stamp(s.now()))
	mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "category", "value": "物理"}, c), 201)
	for _, path := range []string{"/api/gaps", "/api/gaps?q=科研", "/api/gaps?view=saved", "/api/gaps?view=subscribed"} {
		w := request(s, "GET", path, nil, c)
		mustStatus(t, w, 200)
		if strings.Contains(w.Body.String(), "新的科研进展标题") || strings.Contains(w.Body.String(), "PROTECTED_SECRET") {
			t.Fatalf("hidden entry leaked in %s: %s", path, w.Body.String())
		}
		var list struct {
			Total int `json:"total"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &list)
		if list.Total != 0 {
			t.Fatal("count leaked", list.Total)
		}
	}
	for _, cookie := range []string{"", c} {
		w := request(s, "GET", "/api/bootstrap", nil, cookie)
		mustStatus(t, w, 200)
		var boot struct {
			Stats map[string]int `json:"stats"`
			User  *User          `json:"user"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &boot)
		if boot.Stats["published"] != 0 || boot.Stats["pending"] != 0 {
			t.Fatal("stats leaked", w.Body.String())
		}
		if boot.User != nil && boot.User.Entitlements.DelayHours != 72 {
			t.Fatal("paid bypassed delay")
		}
		mustStatus(t, request(s, "GET", fmt.Sprintf("/api/gaps/%d", g), nil, cookie), 404)
	}
	for _, act := range []string{"unlock", "save"} {
		mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/%s", g, act), nil, c), 404)
	}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/report", g), map[string]string{"reason": "核对原始论文发现具体问题"}, c), 404)
	base := s.now()
	s.now = func() time.Time { return base.Add(time.Second) }
	mustStatus(t, request(s, "GET", fmt.Sprintf("/api/gaps/%d", g), nil, c), 200)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, c), 200)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, c), 200)
	var credits, used, n int
	_ = s.db.QueryRow("SELECT credits,daily_used FROM users WHERE id=?", id).Scan(&credits, &used)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM unlocks WHERE user_id=?", id).Scan(&n)
	if credits != 10 || used != 1 || n != 1 {
		t.Fatalf("boundary/repeat changed charge %d %d %d", credits, used, n)
	}
}
func TestDelayScheduleAndStaffOverride(t *testing.T) {
	s := testServer(t)
	schedule := []int{72, 48, 24, 12, 6, 3, 1, 0, 0, 0}
	for i, tier := range tiers {
		u := s.decorate(&User{Role: "member", Points: tier.MinPoints, MembershipUntil: stamp(s.now().Add(time.Hour))})
		if u.Entitlements.DelayHours != schedule[i] || tier.DelayHours != schedule[i] {
			t.Fatalf("tier %d incorrect delay", i)
		}
	}
	for _, role := range []string{"admin", "reviewer"} {
		u := s.decorate(&User{Role: role})
		if u.Entitlements.DelayHours != 0 {
			t.Fatal("staff delayed")
		}
	}
}
func TestAdminSeesEveryStateWithoutCreditsOrDelay(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	author, member := account(t, s, "author@example.org", "member", 0)
	for _, status := range []string{"pending", "published", "rejected", "expired", "disputed"} {
		g := gap(t, s, "exclusive", status, author)
		_, _ = s.db.Exec("UPDATE gaps SET released_at=? WHERE id=?", stamp(s.now().Add(time.Hour)), g)
		w := request(s, "GET", fmt.Sprintf("/api/gaps/%d", g), nil, admin)
		mustStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), "PROTECTED_SECRET") {
			t.Fatal("admin lost full text", status)
		}
		mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, admin), 200)
	}
	w := request(s, "GET", "/api/gaps?status=all", nil, admin)
	mustStatus(t, w, 200)
	var list struct {
		Total int   `json:"total"`
		Items []Gap `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if list.Total != 5 || len(list.Items) != 5 {
		t.Fatal("all states missing", w.Body.String())
	}
	mustStatus(t, request(s, "GET", "/api/gaps?status=all", nil, member), 403)
	w = request(s, "GET", "/api/admin/overview", nil, admin)
	var overview struct {
		Content []Gap `json:"content"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &overview)
	if len(overview.Content) != 5 {
		t.Fatal("overview states missing")
	}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/users/%d", author), map[string]string{"role": "admin"}, admin), 400)
}
func TestApprovalReleaseTimePreservedAndUnlockSurvivesDowngrade(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 10)
	id, reader := account(t, s, "reader@example.org", "member", 10)
	_, _ = s.db.Exec("UPDATE users SET points=6500 WHERE id=?", id)
	g := gap(t, s, "featured", "pending", nil)
	_, _ = s.db.Exec("UPDATE gaps SET published_at='2020-01-01T00:00:00Z' WHERE id=?", g)
	path := fmt.Sprintf("/api/admin/review/%d", g)
	approvedAt := stamp(s.now())
	mustStatus(t, request(s, "POST", path, map[string]string{"action": "approve", "level": "featured", "note": "核对公开论文中的实验条件"}, admin), 200)
	var release, source string
	_ = s.db.QueryRow("SELECT released_at,published_at FROM gaps WHERE id=?", g).Scan(&release, &source)
	if release != approvedAt || source != "2020-01-01T00:00:00Z" {
		t.Fatal("approval did not separate timestamps", release, source)
	}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, reader), 200)
	_, _ = s.db.Exec("UPDATE users SET points=0 WHERE id=?", id)
	w := request(s, "GET", fmt.Sprintf("/api/gaps/%d", g), nil, reader)
	mustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "PROTECTED_SECRET") {
		t.Fatal("previous unlock lost after downgrade")
	}
	mustStatus(t, request(s, "GET", fmt.Sprintf("/api/gaps/%d", g), nil, ""), 404)
	item := editingItem(t, s, g, admin)
	input := editInput(item, "经重新核对后的科研进展标题")
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/submissions/%d/edit", g), input, admin), 200)
	base := s.now()
	s.now = func() time.Time { return base.Add(30 * time.Minute) }
	rev := editingItem(t, s, g, admin).Revision
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/revisions/%d/review", rev.ID), map[string]string{"action": "approve", "level": "featured", "note": "复核更新的实验条件和结果", "edit_token": rev.EditToken}, admin), 200)
	_ = s.db.QueryRow("SELECT released_at FROM gaps WHERE id=?", g).Scan(&release)
	if release != approvedAt {
		t.Fatal("re-review reset first release")
	}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, reader), 200)
	var credits int
	_ = s.db.QueryRow("SELECT credits FROM users WHERE id=?", id).Scan(&credits)
	if credits != 10 {
		t.Fatal("repeat unlocked charged", credits)
	}
}
func TestReleaseMigrationPreservesPublishedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	s, e := newServer(path, "web", "")
	if e != nil {
		t.Fatal(e)
	}
	g := gap(t, s, "ordinary", "published", nil)
	old := "2025-01-01T00:00:00Z"
	_, _ = s.db.Exec("UPDATE gaps SET created_at=? WHERE id=?", old, g)
	if _, e = s.db.Exec("ALTER TABLE gaps DROP COLUMN released_at"); e != nil {
		t.Fatal(e)
	}
	s.db.Close()
	s, e = newServer(path, "web", "")
	if e != nil {
		t.Fatal(e)
	}
	var released string
	_ = s.db.QueryRow("SELECT released_at FROM gaps WHERE id=?", g).Scan(&released)
	if released != old {
		t.Fatal("release migration wrong", released)
	}
	explicit := "2024-01-01T00:00:00Z"
	_, _ = s.db.Exec("UPDATE gaps SET released_at=? WHERE id=?", explicit, g)
	s.db.Close()
	s, e = newServer(path, "web", "")
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	_ = s.db.QueryRow("SELECT released_at FROM gaps WHERE id=?", g).Scan(&released)
	if released != explicit {
		t.Fatal("reboot overwrote explicit seed release")
	}
}
