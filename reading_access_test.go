package main

import (
	"fmt"
	"testing"
	"time"
)

// Fixtures persist a paid reading entitlement; contribution points alone never grant access.
func paidReadingFixture(t *testing.T, s *server, user int64, delay int) {
	t.Helper()
	id := "fixture-" + token(8)
	_, e := s.db.Exec(`INSERT INTO billing_orders(id,user_id,plan,name,amount_cents,days,credits,channel,status,expires_at,created_at,updated_at) VALUES(?,?,'reading_1','test',1800,30,0,'alipay','success',?,?,?)`, id, user, stamp(s.now().Add(time.Hour)), stamp(s.now()), stamp(s.now()))
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.db.Exec(`INSERT INTO reading_entitlements(order_id,user_id,plan,delay_hours,starts_at,ends_at,created_at) VALUES(?,?,'reading_1',?,?,?,?)`, id, user, delay, stamp(s.now().Add(-time.Hour)), stamp(s.now().Add(30*24*time.Hour)), stamp(s.now()))
	if e != nil {
		t.Fatal(e)
	}
}
func TestReadingAccessAndNoPermanentUnlockOnSubscription(t *testing.T) {
	s := testServer(t)
	uid, c := account(t, s, "paidreading@example.org", "member", 20)
	g := gap(t, s, "exclusive", "published", nil)
	s.db.Exec("UPDATE gaps SET released_at=? WHERE id=?", stamp(s.now().Add(-8*time.Hour+time.Second)), g)
	paidReadingFixture(t, s, uid, 8)
	mustStatus(t, request(s, "GET", fmt.Sprintf("/api/gaps/%d", g), nil, c), 404)
	base := s.now()
	s.now = func() time.Time { return base.Add(time.Second) }
	w := request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, c)
	mustStatus(t, w, 200)
	var n, credits int
	s.db.QueryRow("SELECT count(*) FROM unlocks WHERE user_id=?", uid).Scan(&n)
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", uid).Scan(&credits)
	if n != 0 || credits != 20 {
		t.Fatal("subscription consumed or granted permanent", n, credits)
	}
}
