package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCentPaymentOnlyFounderAndNoEntitlements(t *testing.T) {
	s, provider, _, member := billingFixture(t)
	owner, admin := account(t, s, founderEmail, "admin", 77)
	mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]string{"plan": "payment_test", "channel": "alipay"}, member), 403)
	mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]any{"plan": "payment_test", "channel": "alipay", "amount_cents": 0}, admin), 400)
	o := billingPurchase(t, s, admin, "payment_test", "alipay")
	if o.Type != "payment_test" || o.Days != 0 || o.Credits != 0 || o.AmountCents != 1 || o.Channel != "alipay" || o.PaymentKind != "qr" {
		t.Fatalf("wrong cent test order: %+v", o)
	}
	if reused := billingPurchase(t, s, admin, "payment_test", "alipay"); reused.ID != o.ID || provider.creates != 1 {
		t.Fatal("test checkout duplicated pending invoice")
	}
	result := billingSuccess(o)
	mustStatus(t, billingCallback(s, result, "test-valid"), 200)
	mustStatus(t, billingCallback(s, result, "test-valid"), 200)
	var credits, grants, creditGrants, ledger int
	var until string
	if err := s.db.QueryRow("SELECT credits,membership_until FROM users WHERE id=?", owner).Scan(&credits, &until); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow("SELECT COUNT(*) FROM billing_grants WHERE user_id=?", owner).Scan(&grants)
	s.db.QueryRow("SELECT COUNT(*) FROM billing_credit_grants WHERE user_id=?", owner).Scan(&creditGrants)
	s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE user_id=?", owner).Scan(&ledger)
	if credits != 77 || until != "" || grants != 0 || creditGrants != 0 || ledger != 0 {
		t.Fatal("test payment granted real entitlements", credits, until, grants, creditGrants, ledger)
	}
	w := request(s, "GET", "/api/billing/orders/"+o.ID, nil, admin)
	mustStatus(t, w, 200)
	var data struct{ Order *billingOrder }
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || data.Order == nil || data.Order.Status != "success" || data.Order.Type != "payment_test" {
		t.Fatal("test receipt missing", w.Body)
	}
	w = request(s, "GET", "/api/billing/plans", nil, admin)
	if strings.Contains(w.Body.String(), "payment_test") {
		t.Fatal("test plan leaked into normal storefront")
	}
	mustStatus(t, request(s, "POST", "/api/admin/test-mode", map[string]any{"tier": 9, "credits": 100}, admin), 200)
	mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]string{"plan": "payment_test", "channel": "alipay"}, admin), 403)
}
