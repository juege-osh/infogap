package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

func coursePaymentFixture(t *testing.T, s *server, price int64, status string, ready bool) int64 {
	t.Helper()
	if err := initCourses(s.db); err != nil {
		t.Fatal(err)
	}
	var owner int64
	if err := s.db.QueryRow("SELECT id FROM users ORDER BY id LIMIT 1").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	at := stamp(s.now())
	r, err := s.db.Exec("INSERT INTO courses(title,summary,topic,price_cents,status,created_by,created_at,updated_at) VALUES('测试课程','只在临时数据库中使用','codex',?,?,?,?,?)", price, status, owner, at, at)
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		if _, err = s.db.Exec("INSERT INTO course_lessons(course_id,title,position,bytes,status,media_key,created_at,updated_at) VALUES(?,'第一课',1,32,'ready','fixture.mp4',?,?)", id, at, at); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestCoursePaymentPlanParsing(t *testing.T) {
	for _, raw := range []string{"course_1", "course_205", "course_9223372036854775807"} {
		if id, ok := parseCoursePlanID(raw); !ok || id <= 0 {
			t.Fatal("valid course plan rejected", raw)
		}
	}
	for _, raw := range []string{"", "1", "course_", "course_0", "course_01", "course_-1", "course_+1", "course_1.0", "course_1e2", "course_１", "course_1 ", " course_1", "course_9223372036854775808", "course_1/../../reading_12", "COURSE_1"} {
		if _, ok := parseCoursePlanID(raw); ok {
			t.Fatal("malformed or aliased course plan accepted", raw)
		}
	}
}

func TestCoursePaymentUsesPublishedServerPriceAndNoContributionDiscount(t *testing.T) {
	s, provider, user, cookie := billingFixture(t)
	s.db.Exec("UPDATE users SET points=2500 WHERE id=?", user)
	id := coursePaymentFixture(t, s, 12345, "published", true)
	o := billingPurchase(t, s, cookie, fmt.Sprintf("course_%d", id), "alipay")
	if o.Type != "course" || o.CourseID != id || o.AmountCents != 12345 || o.ListAmountCents != 12345 || o.DiscountPercent != 100 || o.Days != 0 || o.Credits != 0 || o.Months != 0 || o.UnlimitedReading {
		t.Fatalf("course received wrong rights/price %+v", o)
	}
	s.db.Exec("UPDATE courses SET price_cents=54321 WHERE id=?", id)
	again := billingPurchase(t, s, cookie, o.Plan, "alipay")
	if again.ID != o.ID || again.AmountCents != 12345 || provider.creates != 1 {
		t.Fatal("pending course repriced or duplicated", again)
	}
	mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]any{"plan": o.Plan, "channel": "alipay", "amount_cents": 1}, cookie), 400)
	mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]any{"plan": o.Plan, "channel": "alipay", "course_id": id + 1}, cookie), 400)
	var catalog struct{ Plans []billingPlan }
	w := request(s, "GET", "/api/billing/plans", nil, cookie)
	mustStatus(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil || len(catalog.Plans) != 7 {
		t.Fatal("courses polluted fixed plans", err, w.Body)
	}
}

func TestCoursePaymentRejectsUnavailableAndInvalidProducts(t *testing.T) {
	s, provider, _, cookie := billingFixture(t)
	for _, tc := range []struct {
		price  int64
		status string
		ready  bool
		want   int
	}{{100, "draft", true, 404}, {0, "published", true, 409}, {100, "published", false, 409}, {100000001, "published", true, 409}} {
		id := coursePaymentFixture(t, s, tc.price, tc.status, tc.ready)
		mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]string{"plan": fmt.Sprintf("course_%d", id), "channel": "alipay"}, cookie), tc.want)
	}
	mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]string{"plan": "course_999999", "channel": "alipay"}, cookie), 404)
	for _, plan := range []string{"course_0", "course_01", "course_-1", "course_999999999999999999999999", "course_1/../../reading_12"} {
		mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]string{"plan": plan, "channel": "alipay"}, cookie), 400)
	}
	var orders int
	s.db.QueryRow("SELECT count(*) FROM billing_orders").Scan(&orders)
	if orders != 0 || provider.creates != 0 {
		t.Fatal("unavailable courses reached payment provider", orders, provider.creates)
	}
}

func TestCoursePaymentCallbackSnapshotOwnershipAndSingleGrant(t *testing.T) {
	s, _, user, cookie := billingFixture(t)
	id := coursePaymentFixture(t, s, 1999, "published", true)
	other := coursePaymentFixture(t, s, 8888, "published", true)
	o := billingPurchase(t, s, cookie, fmt.Sprintf("course_%d", id), "alipay")
	for _, mutation := range []string{"amount", "merchant", "channel"} {
		r := billingSuccess(o)
		switch mutation {
		case "amount":
			r.AmountCents = 1
		case "merchant":
			r.MerchantID = "other"
		case "channel":
			r.TradeType = "T_NATIVE"
		}
		mustStatus(t, billingCallback(s, r, "test-valid"), 500)
	}
	mustStatus(t, billingCallback(s, billingSuccess(o), "bad"), 400)
	var count int
	s.db.QueryRow("SELECT count(*) FROM course_access").Scan(&count)
	if count != 0 {
		t.Fatal("bad callback granted course")
	}
	// A paid invoice is honored even after its course is edited or unpublished.
	s.db.Exec("UPDATE courses SET price_cents=99999,status='draft' WHERE id=?", id)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.billingApply(billingSuccess(o), "course-concurrent") }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s.db.QueryRow("SELECT count(*) FROM course_access WHERE user_id=? AND course_id=? AND order_id=?", user, id, o.ID).Scan(&count)
	if count != 1 {
		t.Fatal("course grant duplicated/lost", count)
	}
	var otherCount, credits, points, rank, reading int
	var member string
	s.db.QueryRow("SELECT count(*) FROM course_access WHERE course_id=?", other).Scan(&otherCount)
	s.db.QueryRow("SELECT credits,points,paid_tier,membership_until FROM users WHERE id=?", user).Scan(&credits, &points, &rank, &member)
	s.db.QueryRow("SELECT count(*) FROM reading_entitlements").Scan(&reading)
	if otherCount != 0 || credits != 10 || points != 0 || rank != 0 || member != "" || reading != 0 {
		t.Fatal("course payment granted unrelated access", otherCount, credits, points, rank, member, reading)
	}
	s.db.Exec("UPDATE courses SET status='published' WHERE id=?", id)
	mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]string{"plan": o.Plan, "channel": "alipay"}, cookie), 409)
	_, otherCookie := account(t, s, "course-other@example.org", "member", 0)
	mustStatus(t, request(s, "GET", "/api/billing/orders/"+o.ID, nil, otherCookie), 404)
}

func TestCoursePaymentAtomicGrantAndNoCrossProductAccess(t *testing.T) {
	s, _, user, cookie := billingFixture(t)
	id := coursePaymentFixture(t, s, 999, "published", true)
	for _, plan := range []string{"reading_12", "credits_100"} {
		o := billingPurchase(t, s, cookie, plan, "alipay")
		if err := s.billingApply(billingSuccess(o), "other-"+plan); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	s.db.QueryRow("SELECT count(*) FROM course_access").Scan(&count)
	if count != 0 {
		t.Fatal("reading/credits unlocked courses")
	}
	o := billingPurchase(t, s, cookie, fmt.Sprintf("course_%d", id), "alipay")
	for _, table := range []string{"course_access", "billing_events"} {
		if _, err := s.db.Exec("CREATE TRIGGER fail_course_payment BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT,'course rollback'); END"); err != nil {
			t.Fatal(err)
		}
		if err := s.billingApply(billingSuccess(o), "course-rollback"); err == nil {
			t.Fatal("expected grant rollback")
		}
		var status string
		s.db.QueryRow("SELECT status FROM billing_orders WHERE id=?", o.ID).Scan(&status)
		s.db.QueryRow("SELECT count(*) FROM course_access").Scan(&count)
		if status != "pending" || count != 0 {
			t.Fatal("partial course settlement", status, count)
		}
		s.db.Exec("DROP TRIGGER fail_course_payment")
	}
	if err := s.billingApply(billingSuccess(o), "course-final"); err != nil {
		t.Fatal(err)
	}
	var credits int
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", user).Scan(&credits)
	if credits != 1010 {
		t.Fatal("course consumed purchased credit", credits)
	}
}
