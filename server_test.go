package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testServer(t *testing.T) *server {
	t.Helper()
	s, e := newServer(filepath.Join(t.TempDir(), "db.sqlite"), "web", "http://example.com")
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { s.db.Close() })
	return s
}
func request(s *server, method, path string, body any, cookie string) *httptest.ResponseRecorder {
	var b bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&b).Encode(body)
	} else {
		b.WriteString("{}")
	}
	r := httptest.NewRequest(method, "http://example.com"+path, &b)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://example.com")
	if cookie != "" {
		r.Header.Set("Cookie", "infogap_session="+cookie)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}
func account(t *testing.T, s *server, email, role string, credits int) (int64, string) {
	t.Helper()
	res, e := s.db.Exec("INSERT INTO users(email,username,role,credits,created_at) VALUES(?,?,?,?,?)", email, strings.Split(email, "@")[0], role, credits, stamp(s.now()))
	if e != nil {
		t.Fatal(e)
	}
	id, _ := res.LastInsertId()
	tok := token(32)
	_, e = s.db.Exec("INSERT INTO sessions(token_hash,user_id,created_at,expires_at) VALUES(?,?,?,?)", digest(tok), id, stamp(s.now()), stamp(s.now().Add(time.Hour)))
	if e != nil {
		t.Fatal(e)
	}
	return id, tok
}
func gap(t *testing.T, s *server, level, status string, author any) int64 {
	t.Helper()
	res, e := s.db.Exec(`INSERT INTO gaps(title,summary,body,category,level,evidence,source_name,source_url,published_at,created_at,updated_at,status,author_id) VALUES('新的科研进展标题','公开预览摘要','PROTECTED_SECRET_FULLTEXT 研究团队公布了新的科研进展，实验条件和适用范围仍需要独立核验。','物理',?,'official','Nature',?,?,?,?,?,?)`, level, "https://www.nature.com/articles/"+token(8), stamp(s.now()), stamp(s.now()), stamp(s.now()), status, author)
	if e != nil {
		t.Fatal(e)
	}
	id, _ := res.LastInsertId()
	if status == "published" || status == "expired" || status == "disputed" {
		at := stamp(s.now().Add(-72 * time.Hour))
		if _, e = s.db.Exec("UPDATE gaps SET released_at=?,published_at=? WHERE id=?", at, at, id); e != nil {
			t.Fatal(e)
		}
	}
	return id
}
func mustStatus(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status=%d want=%d body=%s", w.Code, code, w.Body.String())
	}
}
func TestFounderRequiresVerifiedEmail(t *testing.T) {
	s := testServer(t)
	var code string
	s.mailer = func(_ context.Context, email, c string) error { code = c; return nil }
	w := request(s, "POST", "/api/auth/code", map[string]string{"email": founderEmail}, "")
	mustStatus(t, w, 200)
	if code == "" || strings.Contains(w.Body.String(), code) {
		t.Fatal("OTP missing or exposed")
	}
	w = request(s, "POST", "/api/auth/verify", map[string]string{"email": founderEmail, "code": "badbad", "username": "站长"}, "")
	mustStatus(t, w, 400)
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
	if n != 0 {
		t.Fatal("unverified founder created")
	}
	w = request(s, "POST", "/api/auth/verify", map[string]string{"email": founderEmail, "code": code, "username": "站长"}, "")
	mustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"role":"admin"`) {
		t.Fatal(w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("session cookie insecure")
	}
	w = request(s, "POST", "/api/auth/verify", map[string]string{"email": founderEmail, "code": code, "username": "站长"}, "")
	mustStatus(t, w, 400)
}
func TestInviteRequiredAndSingleUse(t *testing.T) {
	s := testServer(t)
	codes := map[string]string{}
	s.mailer = func(_ context.Context, email, c string) error { codes[email] = c; return nil }
	w := request(s, "POST", "/api/auth/code", map[string]string{"email": "one@example.org"}, "")
	mustStatus(t, w, 403)
	_, e := s.db.Exec("INSERT INTO invites(code,max_uses,expires_at,created_at) VALUES('ABC',1,?,?)", stamp(s.now().Add(time.Hour)), stamp(s.now()))
	if e != nil {
		t.Fatal(e)
	}
	for _, email := range []string{"one@example.org", "two@example.org"} {
		w = request(s, "POST", "/api/auth/code", map[string]string{"email": email, "invite": "ABC"}, "")
		mustStatus(t, w, 200)
	}
	w = request(s, "POST", "/api/auth/verify", map[string]string{"email": "one@example.org", "invite": "ABC", "code": codes["one@example.org"], "username": "用户一"}, "")
	mustStatus(t, w, 200)
	w = request(s, "POST", "/api/auth/verify", map[string]string{"email": "two@example.org", "invite": "ABC", "code": codes["two@example.org"], "username": "用户二"}, "")
	mustStatus(t, w, 403)
	var uses int
	_ = s.db.QueryRow("SELECT uses FROM invites WHERE code='ABC'").Scan(&uses)
	if uses != 1 {
		t.Fatal(uses)
	}
}
func TestOTPAttemptLimit(t *testing.T) {
	s := testServer(t)
	var code string
	s.mailer = func(_ context.Context, email, c string) error { code = c; return nil }
	mustStatus(t, request(s, "POST", "/api/auth/code", map[string]string{"email": founderEmail}, ""), 200)
	for range 5 {
		mustStatus(t, request(s, "POST", "/api/auth/verify", map[string]string{"email": founderEmail, "code": "xxxxxx", "username": "站长"}, ""), 400)
	}
	mustStatus(t, request(s, "POST", "/api/auth/verify", map[string]string{"email": founderEmail, "code": code, "username": "站长"}, ""), 400)
}
func TestLockedContentAndCreditRace(t *testing.T) {
	s := testServer(t)
	id, c := account(t, s, "reader@example.org", "member", 1)
	g := gap(t, s, "featured", "published", nil)
	path := fmt.Sprintf("/api/gaps/%d", g)
	for _, ck := range []string{"", c} {
		w := request(s, "GET", path, nil, ck)
		mustStatus(t, w, 200)
		if strings.Contains(w.Body.String(), "PROTECTED_SECRET") {
			t.Fatal("body leaked")
		}
		w = request(s, "GET", "/api/gaps", nil, ck)
		if strings.Contains(w.Body.String(), "PROTECTED_SECRET") {
			t.Fatal("list leaked")
		}
	}
	var wg sync.WaitGroup
	errs := make(chan string, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := request(s, "POST", path+"/unlock", nil, c)
			if w.Code != 200 {
				errs <- w.Body.String()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	var credits, count int
	_ = s.db.QueryRow("SELECT credits FROM users WHERE id=?", id).Scan(&credits)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM unlocks WHERE user_id=?", id).Scan(&count)
	if credits != 0 || count != 1 {
		t.Fatalf("credits=%d unlocks=%d", credits, count)
	}
	w := request(s, "GET", path, nil, c)
	if !strings.Contains(w.Body.String(), "PROTECTED_SECRET") {
		t.Fatal("unlocked missing")
	}
	g2 := gap(t, s, "featured", "published", nil)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g2), nil, c), 402)
}
func TestDailyQuotaAndRankGate(t *testing.T) {
	s := testServer(t)
	id, c := account(t, s, "reader@example.org", "member", 10)
	for range 4 {
		g := gap(t, s, "ordinary", "published", nil)
		mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, c), 200)
	}
	var credits, used int
	_ = s.db.QueryRow("SELECT credits,daily_used FROM users WHERE id=?", id).Scan(&credits, &used)
	if credits != 7 || used != 1 {
		t.Fatalf("credits=%d daily=%d", credits, used)
	}
	g := gap(t, s, "rare", "published", nil)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, c), 403)
	_, _ = s.db.Exec("UPDATE users SET points=300,featured_contributions=1 WHERE id=?", id)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, c), 200)
	g = gap(t, s, "exclusive", "published", nil)
	_, _ = s.db.Exec("UPDATE users SET points=2500,rare_contributions=1,last_quality_at=? WHERE id=?", stamp(s.now().Add(-91*24*time.Hour)), id)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, c), 403)
	_, _ = s.db.Exec("UPDATE users SET points=4000,last_quality_at=? WHERE id=?", stamp(s.now()), id)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, c), 200)
}
func TestReviewExactlyOnceAndRefund(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 10)
	author, _ := account(t, s, "writer@example.org", "member", 10)
	reader, ck := account(t, s, "reader@example.org", "member", 10)
	_, _ = s.db.Exec("UPDATE users SET points=6500,day_key=?,daily_used=20 WHERE id=?", dayKey(s.now()), reader)
	g := gap(t, s, "featured", "pending", author)
	path := fmt.Sprintf("/api/admin/review/%d", g)
	in := map[string]string{"action": "approve", "level": "featured", "note": "已核对原始论文与公开材料"}
	mustStatus(t, request(s, "POST", path, in, admin), 200)
	mustStatus(t, request(s, "POST", path, in, admin), 409)
	var credits, contrib int
	_ = s.db.QueryRow("SELECT credits,contributions FROM users WHERE id=?", author).Scan(&credits, &contrib)
	if credits != 210 || contrib != 1 {
		t.Fatalf("%d %d", credits, contrib)
	}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, ck), 200)
	in = map[string]string{"action": "dispute", "note": "原始论文已经撤回，结论不能成立"}
	mustStatus(t, request(s, "POST", path, in, admin), 200)
	mustStatus(t, request(s, "POST", path, in, admin), 409)
	_ = s.db.QueryRow("SELECT credits FROM users WHERE id=?", reader).Scan(&credits)
	if credits != 10 {
		t.Fatal("refund wrong", credits)
	}
	var refundcount int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE kind='refund'").Scan(&refundcount)
	if refundcount != 1 {
		t.Fatal("duplicate refund", refundcount)
	}
}
func TestPrivilegeAndCSRF(t *testing.T) {
	s := testServer(t)
	id, c := account(t, s, "reviewer@example.org", "reviewer", 10)
	g := gap(t, s, "featured", "pending", id)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/review/%d", g), map[string]string{"action": "approve", "level": "featured"}, c), 403)
	mustStatus(t, request(s, "POST", "/api/admin/invites", map[string]int{"days": 7}, c), 403)
	r := httptest.NewRequest("POST", "http://example.com/api/auth/logout", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Cookie", "infogap_session="+c)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	mustStatus(t, w, 403)
	if s.current(r) == nil {
		t.Fatal("CSRF deleted session")
	}
}
func TestPendingNotPublicAndDisabledSession(t *testing.T) {
	s := testServer(t)
	id, c := account(t, s, "reader@example.org", "member", 10)
	g := gap(t, s, "ordinary", "pending", nil)
	mustStatus(t, request(s, "GET", fmt.Sprintf("/api/gaps/%d", g), nil, c), 404)
	_, _ = s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", id)
	mustStatus(t, request(s, "GET", "/api/me/activity", nil, c), 401)
}

func TestDisputeRevokesRewardAndQualityWithoutDebt(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 10)
	author, _ := account(t, s, "writer@example.org", "member", 10)
	g := gap(t, s, "rare", "pending", author)
	path := fmt.Sprintf("/api/admin/review/%d", g)
	mustStatus(t, request(s, "POST", path, map[string]string{"action": "approve", "level": "rare", "note": "核对原始论文、官方补充材料与实验数据"}, admin), 200)
	_, _ = s.db.Exec("UPDATE users SET credits=17 WHERE id=?", author)
	mustStatus(t, request(s, "POST", path, map[string]string{"action": "dispute", "note": "原始证据不支持投稿结论"}, admin), 200)
	mustStatus(t, request(s, "POST", path, map[string]string{"action": "dispute", "note": "原始证据不支持投稿结论"}, admin), 409)
	u, e := userScan(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", author))
	if e != nil {
		t.Fatal(e)
	}
	if u.Credits != 0 || u.Points != 0 || u.Contributions != 0 || u.FeaturedContributions != 0 || u.RareContributions != 0 || u.LastQualityAt != "" {
		t.Fatalf("reward not revoked: %+v", u)
	}
	var amount, count int
	_ = s.db.QueryRow("SELECT amount FROM ledger WHERE kind='reward_revoke' AND user_id=?", author).Scan(&amount)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE kind='reward_revoke' AND user_id=?", author).Scan(&count)
	if amount != -17 || count != 1 {
		t.Fatalf("amount=%d count=%d", amount, count)
	}
}

func TestStaffEditQueuesAndKeepsPriorUnlocks(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 10)
	_, member := account(t, s, "member@example.org", "member", 10)
	g := gap(t, s, "featured", "published", nil)
	path := fmt.Sprintf("/api/gaps/%d", g)
	mustStatus(t, request(s, "POST", path+"/unlock", nil, member), 200)
	input := map[string]any{"title": "经重新核对后的科研进展标题", "summary": "核对原始资料后重新整理的中文摘要，说明这项研究的证据范围。", "body": strings.Repeat("这是经过编辑核对的完整正文与证据限制。", 10), "category": "物理", "evidence": "peer_reviewed", "tags": []string{"量子"}}
	editPath := fmt.Sprintf("/api/admin/gaps/%d", g)
	mustStatus(t, request(s, "POST", editPath, input, member), 403)
	mustStatus(t, request(s, "POST", editPath, input, admin), 200)
	mustStatus(t, request(s, "GET", path, nil, ""), 404)
	w := request(s, "GET", path, nil, member)
	mustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "这是经过编辑") {
		t.Fatal("prior unlock lost")
	}
	w = request(s, "GET", "/api/gaps?view=unlocked", nil, member)
	if !strings.Contains(w.Body.String(), "这是经过编辑") {
		t.Fatal("history omitted")
	}
	var count int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM audit WHERE action='edit_gap'").Scan(&count)
	if count != 1 {
		t.Fatal("missing edit audit")
	}
}
