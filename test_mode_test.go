package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func previewUser(t *testing.T, s *server, cookie string) *User {
	t.Helper()
	w := request(s, "GET", "/api/bootstrap", nil, cookie)
	mustStatus(t, w, 200)
	var data struct{ User *User }
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.User == nil {
		t.Fatal("missing current user")
	}
	return data.User
}
func TestTestModeRequiresOriginalFounderAndPreservesAdmin(t *testing.T) {
	s := testServer(t)
	owner, cookie := account(t, s, founderEmail, "admin", 99)
	_, member := account(t, s, "regular@example.org", "member", 10)
	_, reviewer := account(t, s, "reviewer@example.org", "reviewer", 10)
	input := map[string]any{"tier": 0, "credits": 100, "reset": true}
	for _, c := range []string{"", member, reviewer} {
		w := request(s, "POST", "/api/admin/test-mode", input, c)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal("non-owner entered test mode", w.Code)
		}
	}
	second := token(32)
	if _, err := s.db.Exec("INSERT INTO sessions(token_hash,user_id,created_at,expires_at) VALUES(?,?,?,?)", digest(second), owner, stamp(s.now()), stamp(s.now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "POST", "/api/admin/test-mode", input, cookie), 200)
	u := previewUser(t, s, cookie)
	if u.ID == owner || u.Role != "member" || !u.TestMode || !u.CanTestMode || u.TierIndex != 0 || u.Entitlements.DelayHours != 0 || u.DailyRemaining != 1 {
		t.Fatalf("bad test identity: %+v", u)
	}
	other := previewUser(t, s, second)
	if other.ID != owner || other.Role != "admin" || other.TestMode {
		t.Fatal("test mode leaked across sessions")
	}
	mustStatus(t, request(s, "GET", "/api/admin/overview", nil, cookie), 403)
	denied := request(s, "GET", "/api/admin/overview", nil, cookie)
	var problem struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(denied.Body.Bytes(), &problem); err != nil || !strings.HasPrefix(problem.RequestID, "RQ-") || problem.RequestID != denied.Header().Get("X-Request-ID") {
		t.Fatal("support reference missing or inconsistent")
	}
	for _, p := range []string{"/api/billing/orders", "/api/submissions", "/api/gaps/1/report"} {
		mustStatus(t, request(s, "POST", p, map[string]any{}, cookie), 403)
	}
	mustStatus(t, request(s, "POST", "/api/auth/code", map[string]string{"email": u.Email}, ""), 403)
	var role string
	var credits int
	s.db.QueryRow("SELECT role,credits FROM users WHERE id=?", owner).Scan(&role, &credits)
	if role != "admin" || credits != 99 {
		t.Fatal("real account was modified")
	}
	mustStatus(t, request(s, "DELETE", "/api/admin/test-mode", nil, cookie), 200)
	if current := previewUser(t, s, cookie); current.ID != owner || current.Role != "admin" || current.TestMode {
		t.Fatal("admin could not return")
	}
	mustStatus(t, request(s, "GET", "/api/admin/overview", nil, cookie), 200)
}
func TestTestModeRanksRealReadingAndReset(t *testing.T) {
	s := testServer(t)
	owner, cookie := account(t, s, founderEmail, "admin", 99)
	old := gap(t, s, "ordinary", "published", nil)
	fresh := gap(t, s, "ordinary", "published", nil)
	s.db.Exec("UPDATE gaps SET released_at=? WHERE id=?", stamp(s.now()), fresh)
	for i := range tiers {
		mustStatus(t, request(s, "POST", "/api/admin/test-mode", map[string]any{"tier": i, "credits": 20, "member": false, "quality": false, "reset": true}, cookie), 200)
		u := previewUser(t, s, cookie)
		if u.TierIndex != i || u.Role != "member" || u.Entitlements.DelayHours != 0 || u.DailyRemaining != 1 || u.Credits != 20 {
			t.Fatal("rank simulation differs from real entitlement", i, u)
		}
		w := request(s, "GET", fmt.Sprintf("/api/gaps/%d", fresh), nil, cookie)
		mustStatus(t, w, 200)
		if strings.Contains(w.Body.String(), "PROTECTED_SECRET_FULLTEXT") {
			t.Fatal("unpaid preview exposed body")
		}

	}
	mustStatus(t, request(s, "POST", "/api/admin/test-mode", map[string]any{"tier": 0, "credits": 0, "reset": true}, cookie), 200)
	unlocked := request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", old), nil, cookie)
	mustStatus(t, unlocked, 200)
	var unlockedResponse struct{ User *User }
	if err := json.Unmarshal(unlocked.Body.Bytes(), &unlockedResponse); err != nil || unlockedResponse.User == nil || !unlockedResponse.User.TestMode || !unlockedResponse.User.CanTestMode {
		t.Fatal("unlock response lost test mode controls")
	}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", old), nil, cookie), 200)
	if u := previewUser(t, s, cookie); u.DailyRemaining != 0 || !u.TestMode {
		t.Fatal("preview did not debit daily credit")
	}
	mustStatus(t, request(s, "POST", "/api/admin/test-mode", map[string]any{"tier": 0, "credits": 0, "member": true, "quality": true, "reset": true}, cookie), 200)
	u := previewUser(t, s, cookie)
	if !u.IsMember || u.Entitlements.SubscriptionLimit != 0 || u.RareContributions != 1 || u.DailyRemaining != 1 {
		t.Fatal("test options not applied")
	}
	w := request(s, "GET", fmt.Sprintf("/api/gaps/%d", old), nil, cookie)
	if strings.Contains(w.Body.String(), "PROTECTED_SECRET_FULLTEXT") {
		t.Fatal("reset left unlocked content")
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM unlocks WHERE user_id=?", owner).Scan(&count)
	if count != 0 {
		t.Fatal("test reading touched administrator")
	}
	for _, input := range []map[string]any{{"tier": -1}, {"tier": 10}, {"tier": 0, "credits": -1}, {"tier": 0, "credits": 100001}, {"tier": 0, "user_id": owner}} {
		mustStatus(t, request(s, "POST", "/api/admin/test-mode", input, cookie), 400)
	}
}

func TestDiagnosticsGroupsAreIsolatedAndRestricted(t *testing.T) {
	s := testServer(t)
	owner, cookie := account(t, s, founderEmail, "admin", 99)
	_, member := account(t, s, "not-admin@example.org", "member", 10)
	for _, c := range []string{"", member} {
		w := request(s, "POST", "/api/admin/diagnostics", map[string]string{"group": "all"}, c)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal("diagnostic data disclosed to non-admin")
		}
	}
	for _, group := range []string{"all", "auth", "reading", "subscriptions", "payment", "service"} {
		w := request(s, "POST", "/api/admin/diagnostics", map[string]string{"group": group}, cookie)
		mustStatus(t, w, 200)
		var result struct {
			Passed     bool
			Checks     []diagnosticCheck
			ID, Notice string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || (!result.Passed && group != "all" && group != "service") || len(result.Checks) == 0 || !strings.HasPrefix(result.ID, "DG-") {
			t.Fatalf("self check failed %s: %s", group, w.Body)
		}
		for _, check := range result.Checks {
			if check.Status == "failed" {
				t.Fatalf("diagnostic failed %s: %+v", group, check)
			}
		}
		for _, table := range []string{"billing_orders", "billing_grants", "billing_credit_grants", "gaps", "subscriptions", "email_codes", "code_requests"} {
			var n int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
				t.Fatal("diagnostics changed production table", table, n, err)
			}
		}
		var credits int
		s.db.QueryRow("SELECT credits FROM users WHERE id=?", owner).Scan(&credits)
		if credits != 99 {
			t.Fatal("diagnostics changed real balance")
		}
	}
	mustStatus(t, request(s, "POST", "/api/admin/diagnostics", map[string]string{"group": "unknown"}, cookie), 400)
	mustStatus(t, request(s, "POST", "/api/admin/test-mode", map[string]any{"tier": 0, "credits": 10}, cookie), 200)
	mustStatus(t, request(s, "POST", "/api/admin/diagnostics", map[string]string{"group": "payment"}, cookie), 200)
}
