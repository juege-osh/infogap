package main

import (
	"testing"
	"time"
)

// These invoices predate removal of membership checkout. Tests still verify
// callback recovery and idempotency for real historical product snapshots.
func historicalBillingOrder(t *testing.T, s *server, cookie, plan, channel string) *billingOrder {
	t.Helper()
	var uid int64
	if e := s.db.QueryRow("SELECT user_id FROM sessions WHERE token_hash=?", digest(cookie)).Scan(&uid); e != nil {
		t.Fatal(e)
	}
	old, e := billingScan(s.db.QueryRow("SELECT "+billingOrderCols+" FROM billing_orders WHERE user_id=? AND plan=? AND status='pending'", uid, plan))
	if e == nil {
		return old
	}
	amount := int64(990)
	days := 30
	if plan == "yearly" {
		amount = 9900
		days = 365
	}
	id := newPaymentNumber("L")
	at := stamp(s.now())
	payload := ""
	if f, ok := s.billing.provider.(*fakeBillingProvider); ok {
		r, e := f.CreatePayment(id, "历史快照", amount, channel, s.now().Add(30*time.Minute))
		if e == nil {
			payload = r.JumpURL
		}
	}
	_, e = s.db.Exec("INSERT INTO billing_orders(id,user_id,plan,name,amount_cents,days,channel,pay_payload,expires_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)", id, uid, plan, "历史会员订单", amount, days, channel, payload, stamp(s.now().Add(30*time.Minute)), at, at)
	if e != nil {
		t.Fatal(e)
	}
	o, e := s.billingLoad(t.Context(), id, uid)
	if e != nil {
		t.Fatal(e)
	}
	return o
}
