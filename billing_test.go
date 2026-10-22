package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBillingProvider struct {
	creates         int
	createErr       error
	createPayload   string
	createTradeType string
	query           huifuPaymentResult
	queryErr        error
}

func (f *fakeBillingProvider) CreatePayment(id, subject string, amount int64, channel string, expires time.Time) (huifuPaymentResult, error) {
	f.creates++
	payload := f.createPayload
	if payload == "" {
		payload = "alipays://platformapi/startapp?appId=test"
	}
	return huifuPaymentResult{MerchantID: "test-merchant", PaymentNo: id, AmountCents: amount, TradeType: f.createTradeType, JumpURL: payload}, f.createErr
}
func (f *fakeBillingProvider) QueryPayment(string, int64) (huifuPaymentResult, error) {
	return f.query, f.queryErr
}
func (f *fakeBillingProvider) VerifyNotification(raw, sig string) (huifuPaymentResult, error) {
	if sig != "test-valid" {
		return huifuPaymentResult{}, errors.New("invalid signature")
	}
	var result huifuPaymentResult
	err := json.Unmarshal([]byte(raw), &result)
	return result, err
}
func billingFixture(t *testing.T) (*server, *fakeBillingProvider, int64, string) {
	s := testServer(t)
	f := &fakeBillingProvider{queryErr: errors.New("no test result")}
	s.billing = &billingService{provider: f, merchantID: "test-merchant"}
	id, cookie := account(t, s, "payer@example.org", "member", 10)
	return s, f, id, cookie
}
func billingPurchase(t *testing.T, s *server, cookie, plan, channel string) *billingOrder {
	t.Helper()
	w := request(s, "POST", "/api/billing/orders", map[string]string{"plan": plan, "channel": channel}, cookie)
	if w.Code != 200 && w.Code != 201 && w.Code != 202 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var result struct {
		Order *billingOrder `json:"order"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil || result.Order == nil {
		t.Fatal("order missing", e)
	}
	return result.Order
}
func billingSuccess(o *billingOrder) huifuPaymentResult {
	return huifuPaymentResult{MerchantID: "test-merchant", PaymentNo: o.ID, PlatformTradeNo: "trade-" + o.ID, AmountCents: o.AmountCents, TransactionStatus: "S", OrderStatus: "1"}
}
func billingCallback(s *server, result huifuPaymentResult, sig string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(result)
	form := url.Values{"resp_data": {string(raw)}, "sign": {sig}}
	r := httptest.NewRequest("POST", "http://example.com/api/billing/notify/huifu", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://payment-gateway.example")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}
func TestBillingPlansDisabledNoMock(t *testing.T) {
	t.Setenv("PAY_PROVIDER", "mock")
	s := testServer(t)
	_, cookie := account(t, s, "disabled@example.org", "member", 10)
	w := request(s, "GET", "/api/billing/plans", nil, "")
	mustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"enabled":false`) || !strings.Contains(w.Body.String(), `"amount_cents":8800`) {
		t.Fatal(w.Body)
	}
	mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]string{"plan": "monthly", "channel": "alipay"}, cookie), 503)
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM billing_orders").Scan(&n)
	if n != 0 {
		t.Fatal("disabled checkout created an order")
	}
}
func TestBillingCheckoutPriceOwnershipReuse(t *testing.T) {
	s, f, _, cookie := billingFixture(t)
	o := billingPurchase(t, s, cookie, "reading_1", "alipay")
	if o.AmountCents != 1800 || o.Days != 30 || len(o.ID) > 32 || o.PayURL != "" || o.PaymentKind != "qr" || o.QRPayload == "" {
		t.Fatalf("wrong order: %+v", o)
	}
	again := billingPurchase(t, s, cookie, "reading_1", "alipay")
	if again.ID != o.ID || f.creates != 1 {
		t.Fatal("duplicate checkout")
	}
	other := billingPurchase(t, s, cookie, "credits_100", "alipay")
	if other.ID == o.ID || f.creates != 2 {
		t.Fatal("different plan reused the wrong invoice")
	}
	_, otherCookie := account(t, s, "otherpayer@example.org", "member", 10)
	mustStatus(t, request(s, "GET", "/api/billing/orders/"+o.ID, nil, otherCookie), 404)
	mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]any{"plan": "reading_1", "channel": "alipay", "amount_cents": 1}, cookie), 400)
}

func TestBillingOnlyAdvertisesAndCreatesAlipay(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		s, provider, _, cookie := billingFixture(t)
		if !enabled {
			s.billing.provider = nil
		}
		w := request(s, "GET", "/api/billing/plans", nil, "")
		mustStatus(t, w, 200)
		var catalog struct{ Channels []string }
		if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil || len(catalog.Channels) != 1 || catalog.Channels[0] != "alipay" {
			t.Fatal("catalog advertised another payment channel", w.Body.String())
		}
		for _, channel := range []string{"wechat", "weixin", "WECHAT", "ALIPAY", " alipay ", "", "bank", "alipay,wechat"} {
			w = request(s, "POST", "/api/billing/orders", map[string]string{"plan": "credits_100", "channel": channel}, cookie)
			mustStatus(t, w, 400)
			if !strings.Contains(w.Body.String(), "仅支持支付宝") {
				t.Fatal("channel rejection was not explicit", w.Body.String())
			}
		}
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM billing_orders").Scan(&count); err != nil || count != 0 || provider.creates != 0 {
			t.Fatal("invalid channel reached the provider or created an order")
		}
	}
}

func TestBillingAlipayPayloadValidation(t *testing.T) {
	for _, valid := range []string{"alipays://platformapi/startapp?appId=test", "https://qr.alipay.com/test", "https://checkout.example.org/signed-huifu-entry?order=123"} {
		if !billingPayPayload(valid) {
			t.Fatal("valid Alipay or signed intermediary payload was rejected", valid)
		}
	}
	for _, invalid := range []string{"weixin://wxpay/bizpayurl?pr=test", "WeIxIn://wxpay/bizpayurl?pr=test", "https://weixin.qq.com/q/test", "https://pay.weixin.qq.com/test", "https://wx.tenpay.com/test", "https://wx.qq.com/test", "https://WEIXIN.QQ.COM./test", "http://qr.alipay.com/test", "https://user:pass@checkout.example.org/test", "javascript:alert(1)", "data:text/plain,test", "https://qr.alipay.com/test\n", ""} {
		if billingPayPayload(invalid) {
			t.Fatal("invalid or WeChat payload was accepted", invalid)
		}
	}
}

func TestBillingNeverExposesWeChatPayloadForAlipayOrder(t *testing.T) {
	for _, tc := range []struct{ payload, tradeType string }{
		{"weixin://wxpay/bizpayurl?pr=test", ""},
		{"https://weixin.qq.com/q/test", "A_NATIVE"},
		{"https://checkout.example.org/pay", "T_NATIVE"},
	} {
		s, provider, uid, cookie := billingFixture(t)
		provider.createPayload, provider.createTradeType = tc.payload, tc.tradeType
		w := request(s, "POST", "/api/billing/orders", map[string]string{"plan": "credits_100", "channel": "alipay"}, cookie)
		mustStatus(t, w, 202)
		var reply struct{ Order billingOrder }
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Order.Status != "pending" || reply.Order.Channel != "alipay" || reply.Order.QRPayload != "" || reply.Order.PayURL != "" {
			t.Fatal("mismatched payment entry was displayed", w.Body.String())
		}
		var payload string
		var balance, grants int
		s.db.QueryRow("SELECT pay_payload FROM billing_orders WHERE id=?", reply.Order.ID).Scan(&payload)
		s.db.QueryRow("SELECT credits FROM users WHERE id=?", uid).Scan(&balance)
		s.db.QueryRow("SELECT COUNT(*) FROM billing_credit_grants").Scan(&grants)
		if payload != "" || balance != 10 || grants != 0 {
			t.Fatal("bad checkout persisted a payment entry or granted credits")
		}
		// A stale mislabelled invoice from an older version must not bypass the
		// new payload check through the pending-order reuse/read paths.
		if _, err := s.db.Exec("UPDATE billing_orders SET pay_payload='weixin://wxpay/bizpayurl?pr=legacy' WHERE id=?", reply.Order.ID); err != nil {
			t.Fatal(err)
		}
		reused := billingPurchase(t, s, cookie, "credits_100", "alipay")
		if reused.ID != reply.Order.ID || reused.QRPayload != "" || reused.PayURL != "" || provider.creates != 1 {
			t.Fatal("reused order exposed a mismatched payload or created another charge")
		}
	}
}

func TestBillingHistoricalWeChatOrdersRemainReadableAndReconcilable(t *testing.T) {
	for _, product := range []struct {
		plan    string
		credits int
	}{{"monthly", 0}, {"credits_100", 100}} {
		t.Run(product.plan, func(t *testing.T) {
			s, provider, uid, cookie := billingFixture(t)
			id := "P20261021IGhistoricalwechat"
			at := stamp(s.now())
			_, err := s.db.Exec(`INSERT INTO billing_orders(id,user_id,plan,name,amount_cents,days,credits,channel,pay_payload,expires_at,created_at,updated_at) VALUES(?,?,?,?,990,30,?,'wechat','weixin://wxpay/bizpayurl?pr=legacy',?,?,?)`, id, uid, product.plan, "历史订单", product.credits, stamp(s.now().Add(time.Minute)), at, at)
			if err != nil {
				t.Fatal(err)
			}
			old, err := s.billingLoad(t.Context(), id, uid)
			if err != nil || old.Channel != "wechat" || old.QRPayload != "" || old.PayURL != "" || old.AmountCents != 990 {
				t.Fatal("historical record was lost or relabelled", err)
			}
			var storedPayload string
			if err := s.db.QueryRow("SELECT pay_payload FROM billing_orders WHERE id=?", id).Scan(&storedPayload); err != nil || storedPayload != "weixin://wxpay/bizpayurl?pr=legacy" {
				t.Fatal("historical accounting record was modified")
			}
			mustStatus(t, request(s, "GET", "/api/billing/orders", nil, cookie), 200)
			wrongChannel := billingSuccess(old)
			wrongChannel.TradeType = "A_NATIVE"
			mustStatus(t, billingCallback(s, wrongChannel, "test-valid"), 500)
			provider.queryErr, provider.query = nil, billingSuccess(old)
			provider.query.TradeType = "T_NATIVE"
			w := request(s, "GET", "/api/billing/orders/"+id, nil, cookie)
			mustStatus(t, w, 200)
			var reply struct{ Order billingOrder }
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Order.Status != "success" || reply.Order.Channel != "wechat" || reply.Order.AmountCents != 990 {
				t.Fatal("verified historical payment failed reconciliation", w.Body.String())
			}
			for range 2 {
				mustStatus(t, billingCallback(s, provider.query, "test-valid"), 200)
			}
			var credits, points, grants int
			var until string
			s.db.QueryRow("SELECT credits,points,membership_until FROM users WHERE id=?", uid).Scan(&credits, &points, &until)
			s.db.QueryRow("SELECT (SELECT COUNT(*) FROM billing_grants)+(SELECT COUNT(*) FROM billing_credit_grants)").Scan(&grants)
			if credits != 10+product.credits*10 || points != 0 || grants != 1 || (product.credits == 0 && until != stamp(s.now().Add(30*24*time.Hour))) || (product.credits > 0 && until != "") {
				t.Fatal("historical payment changed product rights or granted twice")
			}
		})
	}
}
func TestBillingCallbackGrantsOnlySubscriptionOnce(t *testing.T) {
	s, _, uid, cookie := billingFixture(t)
	o := historicalBillingOrder(t, s, cookie, "monthly", "alipay")
	result := billingSuccess(o)
	mustStatus(t, billingCallback(s, result, "bad"), 400)
	var until string
	s.db.QueryRow("SELECT membership_until FROM users WHERE id=?", uid).Scan(&until)
	if until != "" {
		t.Fatal("invalid signature granted membership")
	}
	for range 2 {
		mustStatus(t, billingCallback(s, result, "test-valid"), 200)
	}
	var points, credits, used, n int
	var role string
	s.db.QueryRow("SELECT membership_until,points,credits,weekly_used,role FROM users WHERE id=?", uid).Scan(&until, &points, &credits, &used, &role)
	if until != stamp(s.now().Add(30*24*time.Hour)) || points != 0 || credits != 10 || used != 0 || role != "member" {
		t.Fatalf("unexpected entitlement %s %d %d %d %s", until, points, credits, used, role)
	}
	s.db.QueryRow("SELECT COUNT(*) FROM billing_grants").Scan(&n)
	if n != 1 {
		t.Fatal("duplicate grant", n)
	}
	renewed := historicalBillingOrder(t, s, cookie, "yearly", "alipay")
	mustStatus(t, billingCallback(s, billingSuccess(renewed), "test-valid"), 200)
	s.db.QueryRow("SELECT membership_until FROM users WHERE id=?", uid).Scan(&until)
	if until != stamp(s.now().Add(395*24*time.Hour)) {
		t.Fatal("renewal did not extend existing membership", until)
	}
	u, _ := userScan(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", uid))
	u = s.decorate(u)
	if !u.IsMember || u.ReadingActive || u.Entitlements.DelayHours != 0 {
		t.Fatalf("legacy membership record changed or gained reading access: %+v", u)
	}
}
func TestBillingRejectMismatchedSuccessfulPayment(t *testing.T) {
	mutations := map[string]func(*huifuPaymentResult){"merchant": func(r *huifuPaymentResult) { r.MerchantID = "other" }, "order": func(r *huifuPaymentResult) { r.PaymentNo = "unknown" }, "amount": func(r *huifuPaymentResult) { r.AmountCents = 1 }, "channel": func(r *huifuPaymentResult) { r.TradeType = "T_NATIVE" }, "zero_amount": func(r *huifuPaymentResult) { r.AmountCents = 0 }, "state": func(r *huifuPaymentResult) { r.OrderStatus = "" }, "transaction": func(r *huifuPaymentResult) { r.PlatformTradeNo = "" }, "unknown_status": func(r *huifuPaymentResult) { r.TransactionStatus = "UNKNOWN" }}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s, _, uid, cookie := billingFixture(t)
			o := historicalBillingOrder(t, s, cookie, "monthly", "alipay")
			r := billingSuccess(o)
			mutate(&r)
			if s.billingApply(r, "event") == nil {
				t.Fatal("invalid result accepted")
			}
			var until string
			s.db.QueryRow("SELECT membership_until FROM users WHERE id=?", uid).Scan(&until)
			if until != "" {
				t.Fatal("invalid result granted membership")
			}
		})
	}
}
func TestBillingOnePlatformTransactionCannotFundTwoOrders(t *testing.T) {
	s, _, _, cookie := billingFixture(t)
	a := historicalBillingOrder(t, s, cookie, "monthly", "alipay")
	b := historicalBillingOrder(t, s, cookie, "yearly", "alipay")
	ar, br := billingSuccess(a), billingSuccess(b)
	br.PlatformTradeNo = ar.PlatformTradeNo
	if e := s.billingApply(ar, "a"); e != nil {
		t.Fatal(e)
	}
	if e := s.billingApply(br, "b"); e == nil {
		t.Fatal("same transaction accepted twice")
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM billing_grants").Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}
func TestBillingTimeoutRetainsOrderAndLateSettlement(t *testing.T) {
	s, f, uid, cookie := billingFixture(t)
	f.createErr = errors.New("test timeout")
	o := historicalBillingOrder(t, s, cookie, "monthly", "alipay")
	if o.Status != "pending" || o.PayURL != "" {
		t.Fatal("timeout falsely settled/failed")
	}
	again := historicalBillingOrder(t, s, cookie, "monthly", "alipay")
	if again.ID != o.ID || f.creates != 1 {
		t.Fatal("uncertain checkout repeated upstream")
	}
	s.db.Exec("UPDATE billing_orders SET status='expired' WHERE id=?", o.ID)
	if err := s.billingApply(billingSuccess(o), "late"); err != nil {
		t.Fatal(err)
	}
	var until string
	s.db.QueryRow("SELECT membership_until FROM users WHERE id=?", uid).Scan(&until)
	if until == "" {
		t.Fatal("verified late success lost")
	}
}
func TestBillingStatusDoesNotSettleDifferentQueriedOrder(t *testing.T) {
	s, f, uid, cookie := billingFixture(t)
	a := historicalBillingOrder(t, s, cookie, "monthly", "alipay")
	b := historicalBillingOrder(t, s, cookie, "yearly", "alipay")
	f.queryErr = nil
	f.query = billingSuccess(b)
	mustStatus(t, request(s, "GET", "/api/billing/orders/"+a.ID, nil, cookie), 200)
	var until string
	s.db.QueryRow("SELECT membership_until FROM users WHERE id=?", uid).Scan(&until)
	if until != "" {
		t.Fatal("query settled a different order")
	}
}

func TestBillingStatusConfirmsPaidOrderAndRefreshesUser(t *testing.T) {
	s, f, _, cookie := billingFixture(t)
	o := historicalBillingOrder(t, s, cookie, "monthly", "alipay")
	f.queryErr = nil
	f.query = billingSuccess(o)
	w := request(s, "GET", "/api/billing/orders/"+o.ID, nil, cookie)
	mustStatus(t, w, 200)
	var response struct {
		Order billingOrder `json:"order"`
		User  User         `json:"user"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &response); e != nil {
		t.Fatal(e)
	}
	if response.Order.Status != "success" || !response.User.IsMember || response.Order.QRPayload != "" || response.User.Credits != 10 || response.User.Points != 0 {
		t.Fatalf("incorrect payment status response: %s", w.Body)
	}
}
func TestBillingConcurrentCallbacksGrantOnce(t *testing.T) {
	s, _, _, cookie := billingFixture(t)
	o := historicalBillingOrder(t, s, cookie, "monthly", "alipay")
	result := billingSuccess(o)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.billingApply(result, "same") }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM billing_grants").Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}

func TestBillingCallbackOnlyEmptyBodyIsProbe(t *testing.T) {
	s, _, _, _ := billingFixture(t)
	cases := []struct {
		body, contentType string
		code              int
	}{{"", "", 200}, {`{"resp_data":"not a probe"}`, "application/json", 415}, {"anything", "text/plain", 415}, {"sign=a&sign=b&resp_data=c", "application/x-www-form-urlencoded", 400}, {"resp_data=abc", "application/x-www-form-urlencoded", 400}}
	for _, test := range cases {
		r := httptest.NewRequest("POST", "http://example.com/api/billing/notify/huifu", strings.NewReader(test.body))
		r.Header.Set("Content-Type", test.contentType)
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		mustStatus(t, w, test.code)
	}
}

func TestBillingFailureAndMembershipExpiry(t *testing.T) {
	s, _, uid, cookie := billingFixture(t)
	o := historicalBillingOrder(t, s, cookie, "monthly", "alipay")
	result := billingSuccess(o)
	result.TransactionStatus = "F"
	result.OrderStatus = "2"
	mustStatus(t, billingCallback(s, result, "test-valid"), 200)
	var until, status string
	s.db.QueryRow("SELECT membership_until FROM users WHERE id=?", uid).Scan(&until)
	s.db.QueryRow("SELECT status FROM billing_orders WHERE id=?", o.ID).Scan(&status)
	if until != "" || status != "failed" {
		t.Fatal("failed payment granted membership")
	}
	expired := stamp(s.now().Add(-time.Second))
	s.db.Exec("UPDATE users SET membership_until=? WHERE id=?", expired, uid)
	u, _ := userScan(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", uid))
	u = s.decorate(u)
	if u.IsMember || u.Entitlements.Subscriptions {
		t.Fatal("expired membership retained subscriptions")
	}
}

func TestBillingCreditPlansAndCheckoutUseServerPrices(t *testing.T) {
	s, _, _, cookie := billingFixture(t)
	w := request(s, "GET", "/api/billing/plans", nil, "")
	mustStatus(t, w, 200)
	var catalog struct {
		Plans []billingPlan `json:"plans"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Plans) != 7 {
		t.Fatalf("expected 4 reading plans and 3 credit packs: %s", w.Body)
	}
	for _, test := range []struct {
		id     string
		amount int64
		credit int
	}{{"credits_100", 990, 1000}, {"credits_350", 2900, 3500}, {"credits_1000", 7900, 10000}} {
		o := billingPurchase(t, s, cookie, test.id, "alipay")
		if o.Type != "credits" || o.AmountCents != test.amount || o.Credits != test.credit || o.Days != 0 || o.MembershipUntil != "" {
			t.Fatalf("incorrect credit order: %+v", o)
		}
	}
	mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]any{"plan": "credits_100", "channel": "alipay", "credits": 999999}, cookie), 400)
}

func TestBillingCreditsGrantOnlyStoredQuantityAndRefreshUser(t *testing.T) {
	s, f, uid, cookie := billingFixture(t)
	until := stamp(s.now().Add(20 * 24 * time.Hour))
	if _, err := s.db.Exec("UPDATE users SET membership_until=?,points=120 WHERE id=?", until, uid); err != nil {
		t.Fatal(err)
	}
	o := billingPurchase(t, s, cookie, "credits_350", "alipay")
	// The stored order remains authoritative even after a catalog product is renamed/retired.
	if _, err := s.db.Exec("UPDATE billing_orders SET plan='retired_credit_pack' WHERE id=?", o.ID); err != nil {
		t.Fatal(err)
	}
	f.queryErr, f.query = nil, billingSuccess(o)
	w := request(s, "GET", "/api/billing/orders/"+o.ID, nil, cookie)
	mustStatus(t, w, 200)
	var response struct {
		Order billingOrder `json:"order"`
		User  User         `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Order.Type != "credits" || response.Order.Status != "success" || response.Order.Days != 0 || response.User.Credits != 3510 || response.User.Points != 120 || response.User.MembershipUntil != until || response.User.Entitlements.DelayHours != 0 || response.User.ReadingActive {
		t.Fatalf("credit payment altered unrelated rights: %s", w.Body)
	}
	var membershipGrants, creditGrants, ledgerCount, amount int
	s.db.QueryRow("SELECT COUNT(*) FROM billing_grants").Scan(&membershipGrants)
	s.db.QueryRow("SELECT COUNT(*) FROM billing_credit_grants WHERE order_id=?", o.ID).Scan(&creditGrants)
	s.db.QueryRow("SELECT COUNT(*),COALESCE(SUM(amount),0) FROM ledger WHERE user_id=? AND kind='purchase'", uid).Scan(&ledgerCount, &amount)
	if membershipGrants != 0 || creditGrants != 1 || ledgerCount != 1 || amount != 3500 {
		t.Fatalf("unexpected grants/ledger: %d %d %d %d", membershipGrants, creditGrants, ledgerCount, amount)
	}
}

func TestBillingCreditsRejectInvalidOrFailedPayments(t *testing.T) {
	for _, mode := range []string{"signature", "amount", "merchant", "channel", "failed"} {
		t.Run(mode, func(t *testing.T) {
			s, _, uid, cookie := billingFixture(t)
			o := billingPurchase(t, s, cookie, "credits_100", "alipay")
			r, signature, code := billingSuccess(o), "test-valid", 500
			switch mode {
			case "signature":
				signature, code = "bad", 400
			case "amount":
				r.AmountCents--
			case "merchant":
				r.MerchantID = "another-merchant"
			case "channel":
				r.TradeType = "T_NATIVE"
			case "failed":
				r.TransactionStatus, r.OrderStatus, code = "F", "2", 200
			}
			mustStatus(t, billingCallback(s, r, signature), code)
			var credits, grants, entries int
			var until string
			s.db.QueryRow("SELECT credits,membership_until FROM users WHERE id=?", uid).Scan(&credits, &until)
			s.db.QueryRow("SELECT COUNT(*) FROM billing_credit_grants").Scan(&grants)
			s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE kind='purchase'").Scan(&entries)
			if credits != 10 || until != "" || grants != 0 || entries != 0 {
				t.Fatal("invalid or failed payment granted credit", credits, until, grants, entries)
			}
		})
	}
}

func TestBillingCreditsConcurrentDuplicateAndLateFailure(t *testing.T) {
	s, _, uid, cookie := billingFixture(t)
	o := billingPurchase(t, s, cookie, "credits_1000", "alipay")
	r := billingSuccess(o)
	var wg sync.WaitGroup
	errors := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- s.billingApply(r, "same-credit-notification") }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	r.TransactionStatus, r.OrderStatus = "F", "2"
	mustStatus(t, billingCallback(s, r, "test-valid"), 200)
	var credits, points, grants, entries int
	var until, role string
	s.db.QueryRow("SELECT credits,points,membership_until,role FROM users WHERE id=?", uid).Scan(&credits, &points, &until, &role)
	s.db.QueryRow("SELECT COUNT(*) FROM billing_credit_grants").Scan(&grants)
	s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE kind='purchase'").Scan(&entries)
	if credits != 10010 || points != 0 || until != "" || role != "member" || grants != 1 || entries != 1 {
		t.Fatalf("duplicate or late failure altered grants: %d %d %s %s %d %d", credits, points, until, role, grants, entries)
	}
}

func TestBillingCreditSettlementRollsBackWholeTransaction(t *testing.T) {
	s, _, uid, cookie := billingFixture(t)
	o := billingPurchase(t, s, cookie, "credits_100", "alipay")
	if _, err := s.db.Exec("CREATE TRIGGER credit_failure BEFORE UPDATE OF credits ON users BEGIN SELECT RAISE(ABORT,'test credit failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.billingApply(billingSuccess(o), "rollback"); err == nil {
		t.Fatal("expected settlement error")
	}
	var credits, grants, entries int
	var status string
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", uid).Scan(&credits)
	s.db.QueryRow("SELECT status FROM billing_orders WHERE id=?", o.ID).Scan(&status)
	s.db.QueryRow("SELECT COUNT(*) FROM billing_credit_grants").Scan(&grants)
	s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE kind='purchase'").Scan(&entries)
	if credits != 10 || status != "pending" || grants != 0 || entries != 0 {
		t.Fatalf("partial settlement survived rollback: %d %s %d %d", credits, status, grants, entries)
	}
}

func TestBillingCreditMigrationPreservesExistingMembershipOrders(t *testing.T) {
	s, f, uid, cookie := billingFixture(t)
	o := historicalBillingOrder(t, s, cookie, "monthly", "alipay")
	if _, err := s.db.Exec("ALTER TABLE billing_orders DROP COLUMN credits"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := InitBilling(s); err != nil {
			t.Fatal(err)
		}
	}
	s.billing = &billingService{provider: f, merchantID: "test-merchant"}
	old, err := s.billingLoad(t.Context(), o.ID, uid)
	if err != nil || old.Credits != 0 || old.Type != "membership" || old.Days != 30 {
		t.Fatalf("legacy order changed: %+v %v", old, err)
	}
	if err := s.billingApply(billingSuccess(old), "legacy"); err != nil {
		t.Fatal(err)
	}
	credit := billingPurchase(t, s, cookie, "credits_100", "alipay")
	if err := s.billingApply(billingSuccess(credit), "migrated-credit"); err != nil {
		t.Fatal(err)
	}
	var balance int
	var until string
	s.db.QueryRow("SELECT credits,membership_until FROM users WHERE id=?", uid).Scan(&balance, &until)
	if balance != 1010 || until != stamp(s.now().Add(30*24*time.Hour)) {
		t.Fatal("migration failed to preserve independent products", balance, until)
	}
}
