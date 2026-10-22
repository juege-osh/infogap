package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// Historical invoices are fixtures, not a route back into retired rank sales.
func legacyRankPurchase(t *testing.T, s *server, cookie, plan, channel string) *billingOrder {
	t.Helper()
	var user int64
	if err := s.db.QueryRow("SELECT user_id FROM sessions WHERE token_hash=?", digest(cookie)).Scan(&user); err != nil {
		t.Fatal(err)
	}
	existing, err := billingScan(s.db.QueryRow("SELECT "+billingOrderCols+" FROM billing_orders WHERE user_id=? AND plan=? AND channel=? AND status='pending'", user, plan, channel))
	if err == nil {
		return existing
	}
	if err != sql.ErrNoRows {
		t.Fatal(err)
	}
	var p billingPlan
	if strings.HasPrefix(plan, "rank_upgrade_") {
		var tier int
		if _, err := fmt.Sscanf(plan, "rank_upgrade_%d", &tier); err != nil {
			t.Fatal(err)
		}
		quote, err := s.rankUpgradePlan(user, tier)
		if err != nil {
			t.Fatal(err)
		}
		p = *quote
	} else {
		var tier, months int
		if _, err := fmt.Sscanf(plan, "rank_%d_%d", &tier, &months); err != nil || tier < 1 || tier > 7 || !hasInt([]int{1, 3, 6, 12}, months) {
			t.Fatal("bad historical rank fixture", plan)
		}
		p = billingPlan{ID: plan, Name: "历史段位订单", AmountCents: rankPrice(tier, months), Days: rankDays(months), RankTier: tier, Months: months, MonthlyCredits: rankGift(tier, months)}
	}
	id, at := newPaymentNumber("P"), stamp(s.now())
	_, err = s.db.Exec(`INSERT INTO billing_orders(id,user_id,plan,name,amount_cents,days,channel,expires_at,created_at,updated_at,rank_tier,months,monthly_credits,rank_snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, user, p.ID, p.Name, p.AmountCents, p.Days, channel, stamp(s.now().Add(30*time.Minute)), at, at, p.RankTier, p.Months, p.MonthlyCredits, p.RankSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.billingLoad(t.Context(), id, user)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// Model residual historical gifts after their accompanying access has ended.
func expireLegacyReading(t *testing.T, s *server, user int64) {
	t.Helper()
	if _, err := s.db.Exec("UPDATE rank_periods SET ends_at=? WHERE user_id=?", stamp(s.now()), user); err != nil {
		t.Fatal(err)
	}
}

func TestReadingCatalogPricesDiscountAndRetiredRanks(t *testing.T) {
	s, _, user, cookie := billingFixture(t)
	for _, tc := range []struct{ points, percent int }{{0, 100}, {99, 100}, {100, 95}, {299, 95}, {300, 92}, {699, 92}, {700, 90}, {1399, 90}, {1400, 85}, {2499, 85}, {2500, 80}, {999999, 80}} {
		if _, err := s.db.Exec("UPDATE users SET points=?,paid_tier=7,paid_until=? WHERE id=?", tc.points, stamp(s.now().AddDate(0, 0, 30)), user); err != nil {
			t.Fatal(err)
		}
		w := request(s, "GET", "/api/billing/plans", nil, cookie)
		mustStatus(t, w, 200)
		var catalog struct {
			Plans           []billingPlan
			DiscountPercent int `json:"discount_percent"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
			t.Fatal(err)
		}
		if len(catalog.Plans) != 7 || catalog.DiscountPercent != tc.percent {
			t.Fatal(w.Body.String())
		}
		for i, p := range catalog.Plans[:4] {
			months, days, price, delay := []int{1, 3, 6, 12}[i], []int{30, 90, 180, 365}[i], []int64{1800, 3800, 5800, 8800}[i], []int{8, 4, 2, 0}[i]
			if p.ID != fmt.Sprintf("reading_%d", months) || p.Type != "reading" || p.Months != months || p.Days != days || p.DelayHours != delay || p.ListAmountCents != price || p.AmountCents != (price*int64(tc.percent)+50)/100 || !p.UnlimitedReading || p.RankTier != 0 || p.MonthlyCredits != 0 {
				t.Fatalf("bad catalog %+v", p)
			}
		}
		for _, p := range catalog.Plans[4:] {
			if p.Type != "credits" || p.DiscountPercent != 100 || p.AmountCents != p.ListAmountCents {
				t.Fatal("contribution discount altered permanent credits", p)
			}
		}
	}
	// Odd cent prices verify final rounding independently of today's whole-yuan catalog.
	if p := pricedBillingPlan(billingPlan{Type: "reading", AmountCents: 101}, 100); p.AmountCents != 96 {
		t.Fatal(p)
	}
	if p := pricedBillingPlan(billingPlan{Type: "reading", AmountCents: 10}, 700); p.AmountCents != 9 {
		t.Fatal(p)
	}
	for _, plan := range []string{"rank_1_1", "rank_7_12", "rank_upgrade_7"} {
		mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]string{"plan": plan, "channel": "alipay"}, cookie), 410)
	}
	mustStatus(t, request(s, "GET", "/api/billing/rank-upgrade?tier=7", nil, cookie), 410)
}

func TestReadingPaymentSnapshotCallbackAndExpiry(t *testing.T) {
	s, provider, user, cookie := billingFixture(t)
	start := s.now()
	s.db.Exec("UPDATE users SET points=300 WHERE id=?", user)
	o := billingPurchase(t, s, cookie, "reading_3", "alipay")
	if o.AmountCents != 3496 || o.DiscountPercent != 92 || o.ListAmountCents != 3800 || o.DelayHours != 4 || o.Type != "reading" {
		t.Fatalf("bad invoice %+v", o)
	}
	s.db.Exec("UPDATE users SET points=2500 WHERE id=?", user)
	again := billingPurchase(t, s, cookie, "reading_3", "alipay")
	if again.ID != o.ID || again.AmountCents != 3496 || provider.creates != 1 {
		t.Fatal("pending snapshot repriced")
	}
	bad := billingSuccess(o)
	bad.AmountCents--
	mustStatus(t, billingCallback(s, bad, "test-valid"), 500)
	mustStatus(t, billingCallback(s, billingSuccess(o), "bad"), 400)
	var n int
	s.db.QueryRow("SELECT count(*) FROM reading_entitlements").Scan(&n)
	if n != 0 {
		t.Fatal("unverified callback granted reading")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.billingApply(billingSuccess(o), "reading-concurrent") }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s.db.QueryRow("SELECT count(*) FROM reading_entitlements").Scan(&n)
	var credits, points, rank, gifts int
	s.db.QueryRow("SELECT credits,points,paid_tier FROM users WHERE id=?", user).Scan(&credits, &points, &rank)
	s.db.QueryRow("SELECT count(*) FROM rank_credit_batches").Scan(&gifts)
	if n != 1 || credits != 10 || points != 2500 || rank != 0 || gifts != 0 {
		t.Fatal(n, credits, points, rank, gifts)
	}
	u := &User{ID: user}
	for _, tc := range []struct {
		at     time.Time
		active bool
	}{{start, true}, {start.Add(90*24*time.Hour - time.Second), true}, {start.Add(90 * 24 * time.Hour), false}} {
		if err := loadReadingAccess(s.db, u, tc.at); err != nil {
			t.Fatal(err)
		}
		if u.ReadingActive != tc.active || (tc.active && (u.ReadingDelayHours != 4 || u.ReadingUntil != stamp(start.Add(90*24*time.Hour)))) {
			t.Fatalf("bad boundary %+v", u)
		}
	}
}

func TestReadingRenewalQueuesAndLegacyCompatibility(t *testing.T) {
	s, _, user, cookie := billingFixture(t)
	start := s.now()
	old := legacyRankPurchase(t, s, cookie, "rank_6_1", "alipay")
	if err := s.billingApply(billingSuccess(old), "legacy"); err != nil {
		t.Fatal(err)
	}
	for _, plan := range []string{"reading_1", "reading_3"} {
		o := billingPurchase(t, s, cookie, plan, "alipay")
		if err := s.billingApply(billingSuccess(o), "reading-renew"); err != nil {
			t.Fatal(err)
		}
	}
	u := &User{ID: user}
	for _, tc := range []struct {
		days, delay int
		active      bool
	}{{0, 1, true}, {29, 1, true}, {30, 8, true}, {59, 8, true}, {60, 4, true}, {149, 4, true}, {150, 0, false}} {
		if err := loadReadingAccess(s.db, u, start.Add(time.Duration(tc.days)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if u.ReadingActive != tc.active || u.ReadingDelayHours != tc.delay || (tc.active && u.ReadingUntil != stamp(start.Add(150*24*time.Hour))) {
			t.Fatalf("day %d: %+v", tc.days, u)
		}
	}
	other, _ := account(t, s, "invite-cache@example.org", "member", 1000)
	s.db.Exec("UPDATE users SET paid_tier=7,paid_until=? WHERE id=?", stamp(start.AddDate(0, 0, 30)), other)
	u = &User{ID: other}
	if err := loadReadingAccess(s.db, u, start); err != nil || u.ReadingActive {
		t.Fatal("invite cache became paid reading", err, u)
	}
}

func TestReadingPaymentAtomicRollbackAndMigration(t *testing.T) {
	s, _, user, cookie := billingFixture(t)
	o := billingPurchase(t, s, cookie, "reading_12", "alipay")
	for _, table := range []string{"reading_entitlements", "billing_events"} {
		if _, err := s.db.Exec("CREATE TRIGGER reading_failure BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT,'test rollback'); END"); err != nil {
			t.Fatal(err)
		}
		if err := s.billingApply(billingSuccess(o), "rollback"); err == nil {
			t.Fatal("expected rollback")
		}
		var status string
		var count int
		s.db.QueryRow("SELECT status FROM billing_orders WHERE id=?", o.ID).Scan(&status)
		s.db.QueryRow("SELECT count(*) FROM reading_entitlements").Scan(&count)
		if status != "pending" || count != 0 {
			t.Fatal("partial reading grant", status, count)
		}
		s.db.Exec("DROP TRIGGER reading_failure")
	}
	if err := s.billingApply(billingSuccess(o), "final"); err != nil {
		t.Fatal(err)
	}
	before, err := s.billingLoad(t.Context(), o.ID, user)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateReadingBilling(s.db); err != nil {
			t.Fatal(err)
		}
	}
	after, err := s.billingLoad(t.Context(), o.ID, user)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("migration rewrote invoice")
	}
}

func TestReadingMigrationKeepsOldPaidAndPendingInvoiceSnapshots(t *testing.T) {
	for _, paid := range []bool{false, true} {
		t.Run(fmt.Sprint(paid), func(t *testing.T) {
			s, _, user, cookie := billingFixture(t)
			o := legacyRankPurchase(t, s, cookie, "rank_2_3", "alipay")
			if paid {
				if err := s.billingApply(billingSuccess(o), "historical-paid"); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.billingLoad(t.Context(), o.ID, user)
			if err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{"DROP TABLE reading_entitlements", "DROP TABLE test_reading_access", "ALTER TABLE billing_orders DROP COLUMN reading_policy_version", "ALTER TABLE billing_orders DROP COLUMN reading_delay_hours", "ALTER TABLE billing_orders DROP COLUMN list_amount_cents", "ALTER TABLE billing_orders DROP COLUMN discount_percent"} {
				if _, err = s.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err = migrateReadingBilling(s.db); err != nil {
					t.Fatal(err)
				}
			}
			after, err := s.billingLoad(t.Context(), o.ID, user)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(before)
			a, _ := json.Marshal(after)
			if string(a) != string(b) {
				t.Fatal("old invoice repriced or rewritten", string(b), string(a))
			}
			if !paid {
				if err = s.billingApply(billingSuccess(after), "historical-paid-after-migration"); err != nil {
					t.Fatal(err)
				}
			}
			u := &User{ID: user}
			if err = loadReadingAccess(s.db, u, s.now()); err != nil || !u.ReadingActive || u.ReadingDelayHours != 8 {
				t.Fatal("old paid order lost access", err, u)
			}
		})
	}
}

func TestReadingLoadFailureClearsCachedUnlimitedAccess(t *testing.T) {
	s, _, user, cookie := billingFixture(t)
	o := billingPurchase(t, s, cookie, "reading_12", "alipay")
	if err := s.billingApply(billingSuccess(o), "paid"); err != nil {
		t.Fatal(err)
	}
	u, err := userScan(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", user))
	if err != nil {
		t.Fatal(err)
	}
	s.decorateWithRank(u)
	if !u.ReadingActive || !u.Entitlements.UnlimitedReading {
		t.Fatal("fixture missing access")
	}
	if _, err = s.db.Exec("DROP TABLE test_reading_access"); err != nil {
		t.Fatal(err)
	}
	if err = loadReadingAccess(s.db, u, s.now()); err == nil || u.ReadingActive || u.ReadingUntil != "" {
		t.Fatal("load failure retained access", err, u)
	}
	u.ReadingActive = true
	s.decorateWithRank(u)
	if u.ReadingActive || u.Entitlements.UnlimitedReading || u.Entitlements.Subscriptions {
		t.Fatal("decoration reused stale access", u)
	}
}

func TestReadingLegacyMembershipCallbacksAndCrossProductRenewal(t *testing.T) {
	for _, orderOfPayment := range [][]string{{"monthly", "reading_1", "yearly", "rank_6_1"}, {"reading_1", "monthly", "rank_6_1", "yearly"}} {
		t.Run(strings.Join(orderOfPayment, "-"), func(t *testing.T) {
			s, _, user, cookie := billingFixture(t)
			start := s.now()
			elapsed := 0
			for i, plan := range orderOfPayment {
				var o *billingOrder
				switch {
				case strings.HasPrefix(plan, "reading_"):
					o = billingPurchase(t, s, cookie, plan, "alipay")
				case strings.HasPrefix(plan, "rank_"):
					o = legacyRankPurchase(t, s, cookie, plan, "alipay")
				default:
					o = historicalBillingOrder(t, s, cookie, plan, "alipay")
				}
				for range 2 {
					mustStatus(t, billingCallback(s, billingSuccess(o), "test-valid"), 200)
				}
				var storedStart, storedEnd string
				table := "billing_grants"
				if strings.HasPrefix(plan, "reading_") {
					table = "reading_entitlements"
				} else if strings.HasPrefix(plan, "rank_") {
					table = "rank_periods"
				}
				if err := s.db.QueryRow("SELECT MIN(starts_at),MAX(ends_at) FROM "+table+" WHERE order_id=?", o.ID).Scan(&storedStart, &storedEnd); err != nil {
					t.Fatal(err)
				}
				if storedStart != stamp(start.Add(time.Duration(elapsed)*24*time.Hour)) {
					t.Fatal("paid time overlapped", plan, storedStart, elapsed)
				}
				elapsed += o.Days
				if storedEnd != stamp(start.Add(time.Duration(elapsed)*24*time.Hour)) {
					t.Fatal("paid duration lost", plan, storedEnd, elapsed)
				}
				u := &User{ID: user}
				if err := loadReadingAccess(s.db, u, start); err != nil || !u.ReadingActive || u.ReadingUntil != storedEnd {
					t.Fatal("legacy access missing", i, err, u)
				}
			}
			// Cache-only/manual membership cannot independently mint a reading pass.
			other, _ := account(t, s, "manual-cache@example.org", "member", 0)
			s.db.Exec("UPDATE users SET membership_until=? WHERE id=?", stamp(start.AddDate(0, 0, 100)), other)
			u := &User{ID: other}
			if err := loadReadingAccess(s.db, u, start); err != nil || u.ReadingActive {
				t.Fatal("cache-only membership became paid", err, u)
			}
		})
	}
}
