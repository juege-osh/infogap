package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSignedNativeCallbackWithoutOptionalOrderStateSettlesExactlyOnce(t *testing.T) {
	for _, plan := range []string{"monthly", "credits_100", "payment_test"} {
		t.Run(plan, func(t *testing.T) {
			s, _, id, cookie := billingFixture(t)
			if plan == "payment_test" {
				id, cookie = account(t, s, founderEmail, "admin", 10)
			}
			var o *billingOrder
			if plan == "monthly" {
				o = historicalBillingOrder(t, s, cookie, plan, "alipay")
			} else {
				o = billingPurchase(t, s, cookie, plan, "alipay")
			}
			client := testHuifuClient(t)
			s.billing = &billingService{provider: client, merchantID: client.huifuID}
			data := huifuSuccessData()
			delete(data, "order_stat")
			data["org_req_seq_id"], data["org_hf_seq_id"], data["trans_type"] = o.ID, "recovery-"+o.ID, "A_NATIVE"
			data["trans_amt"] = fmt.Sprintf("%d.%02d", o.AmountCents/100, o.AmountCents%100)
			raw, _ := json.Marshal(data)
			sign, _ := signHuifuRaw(string(raw), client.privateKey)
			form := url.Values{"resp_data": {string(raw)}, "sign": {sign}}
			for i := 0; i < 2; i++ {
				req := httptest.NewRequest("POST", "http://example.com/api/billing/notify/huifu", strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				w := httptest.NewRecorder()
				s.routes().ServeHTTP(w, req)
				mustStatus(t, w, 200)
			}
			var status, until string
			var credits int
			s.db.QueryRow("SELECT status FROM billing_orders WHERE id=?", o.ID).Scan(&status)
			s.db.QueryRow("SELECT credits,membership_until FROM users WHERE id=?", id).Scan(&credits, &until)
			if status != "success" {
				t.Fatal("verified native callback was not settled")
			}
			if plan == "credits_100" && credits != 1010 {
				t.Fatal("callback duplicated or lost credits", credits)
			}
			if plan == "monthly" && until == "" {
				t.Fatal("membership not granted")
			}
			if plan == "payment_test" && (credits != 10 || until != "") {
				t.Fatal("test order granted rights")
			}
		})
	}
}

func TestBackgroundReconciliationRecoversMissedCallbackSafely(t *testing.T) {
	for _, kind := range []string{"paid", "wrong_order", "wrong_amount", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			s, f, id, cookie := billingFixture(t)
			o := billingPurchase(t, s, cookie, "credits_100", "alipay")
			f.query, f.queryErr = billingSuccess(o), nil
			switch kind {
			case "wrong_order":
				f.query.PaymentNo = "different"
			case "wrong_amount":
				f.query.AmountCents++
			case "timeout":
				f.queryErr = errors.New("temporary gateway failure")
			}
			if err := s.reconcilePayments(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := s.reconcilePayments(context.Background()); err != nil {
				t.Fatal(err)
			}
			var status string
			var credits, grants int
			s.db.QueryRow("SELECT status FROM billing_orders WHERE id=?", o.ID).Scan(&status)
			s.db.QueryRow("SELECT credits FROM users WHERE id=?", id).Scan(&credits)
			s.db.QueryRow("SELECT count(*) FROM billing_credit_grants WHERE user_id=?", id).Scan(&grants)
			if kind == "paid" {
				if status != "success" || credits != 1010 || grants != 1 {
					t.Fatal("background recovery lost or duplicated settlement", status, credits, grants)
				}
			} else if status != "pending" || credits != 10 || grants != 0 {
				t.Fatal("unverified payment was settled", status, credits, grants)
			}
		})
	}
}
