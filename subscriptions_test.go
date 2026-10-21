package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSubscriptionEntitlementsAndLimits(t *testing.T) {
	s := testServer(t)
	id, member := account(t, s, "member@example.org", "member", 10)
	g := gap(t, s, "ordinary", "published", nil)
	mustStatus(t, request(s, "GET", "/api/gaps?q=科研", nil, member), 200)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/save", g), nil, member), 200)
	mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "category", "value": "物理"}, member), 403)
	mustStatus(t, request(s, "GET", "/api/gaps?view=subscribed", nil, member), 403)
	for _, c := range []struct{ points, limit int }{{0, 0}, {700, 3}, {1400, 8}, {2500, 8}, {4000, 20}, {6500, 40}, {15000, 40}} {
		u := s.decorate(&User{Points: c.points, Role: "member"})
		if u.Entitlements.SubscriptionLimit != c.limit || u.Entitlements.Subscriptions != (c.limit > 0) {
			t.Fatalf("points=%d entitlements=%+v", c.points, u.Entitlements)
		}
	}
	_, _ = s.db.Exec("UPDATE users SET points=700 WHERE id=?", id)
	for _, v := range []string{"量子", "CRISPR", "气候"} {
		mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "tag", "value": v}, member), 201)
	}
	mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "tag", "value": "crispr"}, member), 200)
	mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "tag", "value": "材料"}, member), 409)
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM subscriptions WHERE user_id=?", id).Scan(&n)
	if n != 3 {
		t.Fatal("duplicate counted", n)
	}
	other, otherCookie := account(t, s, "other@example.org", "member", 10)
	_, _ = s.db.Exec("UPDATE users SET points=700 WHERE id=?", other)
	var sub int64
	_ = s.db.QueryRow("SELECT id FROM subscriptions WHERE user_id=? LIMIT 1", id).Scan(&sub)
	mustStatus(t, request(s, "DELETE", fmt.Sprintf("/api/subscriptions/%d", sub), nil, otherCookie), 404)
	mustStatus(t, request(s, "DELETE", fmt.Sprintf("/api/subscriptions/%d", sub), nil, member), 200)
}
func TestPaidMembershipExpiryAndNoContentBypass(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 10)
	id, member := account(t, s, "member@example.org", "member", 10)
	_, _ = s.db.Exec("UPDATE sessions SET expires_at=? WHERE user_id=?", stamp(s.now().Add(100*24*time.Hour)), id)
	path := fmt.Sprintf("/api/admin/users/%d", id)
	mustStatus(t, request(s, "POST", path, map[string]int{"membership_days": 30}, admin), 200)
	u, e := userScan(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", id))
	if e != nil {
		t.Fatal(e)
	}
	s.decorate(u)
	if !u.IsMember || u.Entitlements.SubscriptionLimit != 20 || u.Points != 0 || u.Credits != 10 || u.TierIndex != 0 {
		t.Fatalf("membership changed content rights %+v", u)
	}
	g := gap(t, s, "rare", "published", nil)
	_, _ = s.db.Exec("UPDATE gaps SET tags='[\"CRISPR\"]' WHERE id=?", g)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, member), 403)
	mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "tag", "value": "CRISPR"}, member), 201)
	w := request(s, "GET", "/api/gaps?view=subscribed", nil, member)
	mustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "新的科研进展标题") || strings.Contains(w.Body.String(), "PROTECTED_SECRET") {
		t.Fatal("subscription filter/content protection wrong", w.Body.String())
	}
	base := s.now()
	s.now = func() time.Time { return base.Add(31 * 24 * time.Hour) }
	mustStatus(t, request(s, "GET", "/api/gaps?view=subscribed", nil, member), 403)
	mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "tag", "value": "气候"}, member), 403)
	w = request(s, "GET", "/api/subscriptions", nil, member)
	mustStatus(t, w, 200)
	var result struct {
		Items   []Subscription `json:"items"`
		Limit   int            `json:"limit"`
		Enabled bool           `json:"enabled"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Enabled || result.Limit != 0 || len(result.Items) != 1 || result.Items[0].Active {
		t.Fatal("expired config not preserved/paused", w.Body.String())
	}
	mustStatus(t, request(s, "GET", "/api/gaps?q=科研", nil, member), 200)
}
func TestMembershipRenewalAndRevokeAudit(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 10)
	id, member := account(t, s, "member@example.org", "member", 10)
	path := fmt.Sprintf("/api/admin/users/%d", id)
	mustStatus(t, request(s, "POST", path, map[string]int{"membership_days": 30}, member), 403)
	for range 2 {
		mustStatus(t, request(s, "POST", path, map[string]int{"membership_days": 30}, admin), 200)
	}
	var until string
	_ = s.db.QueryRow("SELECT membership_until FROM users WHERE id=?", id).Scan(&until)
	if until != stamp(s.now().Add(60*24*time.Hour)) {
		t.Fatal("renewal did not extend", until)
	}
	mustStatus(t, request(s, "POST", path, map[string]int{"membership_days": 0}, admin), 200)
	_ = s.db.QueryRow("SELECT membership_until FROM users WHERE id=?", id).Scan(&until)
	if until != "" {
		t.Fatal("revoke failed")
	}
	var count int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM audit WHERE action='membership_change'").Scan(&count)
	if count != 3 {
		t.Fatal("membership audit missing", count)
	}
	mustStatus(t, request(s, "POST", path, map[string]int{"membership_days": -1}, admin), 400)
}
func TestConcurrentSubscriptionLimit(t *testing.T) {
	s := testServer(t)
	id, c := account(t, s, "member@example.org", "member", 10)
	_, _ = s.db.Exec("UPDATE users SET points=700 WHERE id=?", id)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := request(s, "POST", "/api/subscriptions", map[string]string{"kind": "tag", "value": fmt.Sprintf("tag%d", i)}, c)
			if w.Code != 201 && w.Code != 409 {
				t.Errorf("unexpected response %d %s", w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	var count int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM subscriptions WHERE user_id=?", id).Scan(&count)
	if count != 3 {
		t.Fatal("limit exceeded", count)
	}
}
func TestMembershipMigrationPreservesUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	s, e := newServer(path, "web", "")
	if e != nil {
		t.Fatal(e)
	}
	id, _ := account(t, s, "legacy@example.org", "member", 123)
	if _, e = s.db.Exec("ALTER TABLE users DROP COLUMN membership_until"); e != nil {
		t.Fatal(e)
	}
	s.db.Close()
	s, e = newServer(path, "web", "")
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	u, e := userScan(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", id))
	if e != nil {
		t.Fatal(e)
	}
	if u.Email != "legacy@example.org" || u.Credits != 123 || u.MembershipUntil != "" {
		t.Fatal("migration lost data", u)
	}
}
func TestMembershipDowngradeRetainsOverflowSubscriptions(t *testing.T) {
	s := testServer(t)
	id, c := account(t, s, "member@example.org", "member", 10)
	_, _ = s.db.Exec("UPDATE users SET points=700,membership_until=? WHERE id=?", stamp(s.now().Add(time.Hour)), id)
	for _, tag := range []string{"one", "two", "three", "four"} {
		mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "tag", "value": tag}, c), 201)
	}
	g := gap(t, s, "ordinary", "published", nil)
	_, _ = s.db.Exec("UPDATE gaps SET tags='[\"four\"]' WHERE id=?", g)
	w := request(s, "GET", "/api/gaps?view=subscribed", nil, c)
	if !strings.Contains(w.Body.String(), "新的科研进展标题") {
		t.Fatal("paid subscription not active")
	}
	_, _ = s.db.Exec("UPDATE users SET membership_until='' WHERE id=?", id)
	w = request(s, "GET", "/api/gaps?view=subscribed", nil, c)
	mustStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "新的科研进展标题") {
		t.Fatal("overflow subscription remained active")
	}
	w = request(s, "GET", "/api/subscriptions", nil, c)
	var response struct {
		Items []Subscription `json:"items"`
		Limit int            `json:"limit"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if len(response.Items) != 4 || response.Limit != 3 || response.Items[3].Active {
		t.Fatal("overflow config lost or active")
	}
}
