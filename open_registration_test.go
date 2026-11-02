package main

import (
	"context"
	"testing"
)

func TestOpenRegistrationGiftExactlyOnce(t *testing.T) {
	s := testServer(t)
	var code string
	s.mailer = func(_ context.Context, _ string, c string) error { code = c; return nil }
	email := "self@example.org"
	mustStatus(t, request(s, "POST", "/api/auth/code", map[string]string{"email": email}, ""), 200)
	input := map[string]string{"email": email, "username": "自行注册", "code": code}
	mustStatus(t, request(s, "POST", "/api/auth/verify", input, ""), 200)
	mustStatus(t, request(s, "POST", "/api/auth/verify", input, ""), 400)
	s.db.Exec("DELETE FROM code_requests")
	mustStatus(t, request(s, "POST", "/api/auth/code", map[string]string{"email": email}, ""), 200)
	input["code"] = code
	mustStatus(t, request(s, "POST", "/api/auth/verify", input, ""), 200)
	var credits, count int
	s.db.QueryRow("SELECT credits FROM users WHERE email=?", email).Scan(&credits)
	s.db.QueryRow("SELECT count(*) FROM ledger WHERE kind='registration_gift'").Scan(&count)
	if credits != 100 || count != 1 {
		t.Fatalf("credits=%d gifts=%d", credits, count)
	}
}
