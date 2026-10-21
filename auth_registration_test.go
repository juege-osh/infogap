package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"
)

func authHTTP(t *testing.T, client *http.Client, origin, path string, input any) (*http.Response, *User) {
	t.Helper()
	method := http.MethodGet
	var body bytes.Buffer
	if input != nil {
		method = http.MethodPost
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, origin+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var output struct{ User *User }
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatalf("invalid auth JSON: %v", err)
	}
	return res, output.User
}

func TestVerifiedRegistrationAutomaticallyLogsInWithSecureCookie(t *testing.T) {
	for _, tc := range []struct{ name, email, invite, role string }{
		{"invited member", "new-member@example.org", "MEMBER-INVITE", "member"},
		{"verified founder", founderEmail, "", "admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			if tc.invite != "" {
				if _, err := s.db.Exec("INSERT INTO invites(code,max_uses,expires_at,created_at) VALUES(?,1,?,?)", tc.invite, stamp(s.now().Add(time.Hour)), stamp(s.now())); err != nil {
					t.Fatal(err)
				}
			}
			var code string
			s.mailer = func(_ context.Context, email, value string) error {
				if email != tc.email {
					t.Fatal("unexpected email recipient")
				}
				code = value
				return nil
			}
			// A real TLS client cookie jar must carry the registration cookie into
			// the next request without manually copying Cookie headers.
			s.secure = true
			host := httptest.NewTLSServer(s.routes())
			defer host.Close()
			s.origin = host.URL
			client := host.Client()
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			client.Jar = jar
			res, user := authHTTP(t, client, host.URL, "/api/bootstrap", nil)
			if res.StatusCode != 200 || user != nil {
				t.Fatal("fresh browser already authenticated")
			}
			res, _ = authHTTP(t, client, host.URL, "/api/auth/code", map[string]string{"email": tc.email, "invite": tc.invite})
			if res.StatusCode != 200 || code == "" {
				t.Fatal("verification code was not sent")
			}
			res, _ = authHTTP(t, client, host.URL, "/api/auth/verify", map[string]string{"email": tc.email, "code": "xxxxxx", "username": "新成员", "invite": tc.invite})
			if res.StatusCode != 400 || len(res.Cookies()) != 0 {
				t.Fatal("incorrect verification created a session")
			}
			if tc.role == "member" {
				res, _ = authHTTP(t, client, host.URL, "/api/auth/verify", map[string]string{"email": tc.email, "code": code, "username": "新成员"})
				if res.StatusCode != 403 || len(res.Cookies()) != 0 {
					t.Fatal("registration bypassed the required invite")
				}
			}
			res, user = authHTTP(t, client, host.URL, "/api/auth/verify", map[string]string{"email": tc.email, "code": code, "username": "新成员", "invite": tc.invite})
			if res.StatusCode != 200 || user == nil || user.ID == 0 || user.Email != tc.email || user.Role != tc.role {
				t.Fatal("verified registration did not return the new user and role")
			}
			cookies := res.Cookies()
			if len(cookies) != 1 {
				t.Fatal("registration did not set exactly one session cookie")
			}
			cookie := cookies[0]
			if cookie.Name != "infogap_session" || len(cookie.Value) != 64 || cookie.Path != "/" || cookie.Domain != "" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 30*24*3600 {
				t.Fatal("registration session cookie is not securely scoped")
			}
			res, loggedIn := authHTTP(t, client, host.URL, "/api/bootstrap", nil)
			if res.StatusCode != 200 || loggedIn == nil || loggedIn.ID != user.ID || loggedIn.Role != tc.role {
				t.Fatal("the browser was not logged in on the request after registration")
			}
			res, _ = authHTTP(t, client, host.URL, "/api/admin/overview", nil)
			want := 403
			if tc.role == "admin" {
				want = 200
			}
			if res.StatusCode != want {
				t.Fatal("new account has incorrect administrative access")
			}
			var sessionHash, expires string
			if err := s.db.QueryRow("SELECT token_hash,expires_at FROM sessions WHERE user_id=?", user.ID).Scan(&sessionHash, &expires); err != nil || sessionHash != digest(cookie.Value) || sessionHash == cookie.Value || expires != stamp(s.now().Add(30*24*time.Hour)) {
				t.Fatal("server session was not persisted safely")
			}
			var count int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM email_codes WHERE email=?", tc.email).Scan(&count); err != nil || count != 0 {
				t.Fatal("successful registration left a reusable verification code")
			}
			if tc.invite != "" {
				if err := s.db.QueryRow("SELECT uses FROM invites WHERE code=?", tc.invite).Scan(&count); err != nil || count != 1 {
					t.Fatal("registration did not consume exactly one invite use")
				}
			}
		})
	}
}

func TestSessionFailureRollsBackRegistrationAndInviteConsumption(t *testing.T) {
	s := testServer(t)
	var code string
	s.mailer = func(_ context.Context, _, value string) error { code = value; return nil }
	if _, err := s.db.Exec("INSERT INTO invites(code,max_uses,expires_at,created_at) VALUES('ATOMIC',1,?,?)", stamp(s.now().Add(time.Hour)), stamp(s.now())); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "POST", "/api/auth/code", map[string]string{"email": "atomic@example.org", "invite": "ATOMIC"}, ""), 200)
	if _, err := s.db.Exec("CREATE TRIGGER fail_registration_session BEFORE INSERT ON sessions BEGIN SELECT RAISE(ABORT,'fixture failure'); END;"); err != nil {
		t.Fatal(err)
	}
	w := request(s, "POST", "/api/auth/verify", map[string]string{"email": "atomic@example.org", "invite": "ATOMIC", "code": code, "username": "原子注册"}, "")
	mustStatus(t, w, 500)
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("failed registration sent an unusable login cookie")
	}
	for _, table := range []string{"users", "sessions", "ledger"} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed session left %s changes: %d", table, count)
		}
	}
	var uses, codes int
	if err := s.db.QueryRow("SELECT uses FROM invites WHERE code='ATOMIC'").Scan(&uses); err != nil || uses != 0 {
		t.Fatal("failed session consumed the invite")
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM email_codes WHERE email='atomic@example.org'").Scan(&codes); err != nil || codes != 1 {
		t.Fatal("failed session consumed the verification code")
	}
}
