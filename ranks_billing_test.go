package main

import (
	"testing"
	"time"
)

func TestRankAnnualGiftsRenewalAndExpiry(t *testing.T) {
	s, _, id, cookie := billingFixture(t)
	start := s.now()
	o := billingPurchase(t, s, cookie, "rank_7_12", "alipay")
	if o.AmountCents != 18800 || o.Days != 365 || o.MonthlyCredits != 14000 {
		t.Fatalf("wrong plan %+v", o)
	}
	for i := 0; i < 2; i++ {
		if e := s.billingApply(billingSuccess(o), "annual"); e != nil {
			t.Fatal(e)
		}
	}
	var credits, count int
	var until string
	s.db.QueryRow("SELECT credits,paid_until FROM users WHERE id=?", id).Scan(&credits, &until)
	if credits != 10 || rankBalance(t, s, id) != 14000 || until != stamp(start.AddDate(0, 0, 365)) {
		t.Fatal(credits, until)
	}
	s.db.QueryRow("SELECT COUNT(*) FROM rank_periods WHERE user_id=?", id).Scan(&count)
	if count != 12 {
		t.Fatal(count)
	}
	// A renewal starts after the purchased year, without granting future credits.
	next := billingPurchase(t, s, cookie, "rank_1_1", "alipay")
	if e := s.billingApply(billingSuccess(next), "renewal"); e != nil {
		t.Fatal(e)
	}
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", id).Scan(&credits)
	if credits != 10 || rankBalance(t, s, id) != 14000 {
		t.Fatal(credits)
	}
	s.now = func() time.Time { return start.AddDate(0, 0, 366) }
	if e := s.syncRankUser(id); e != nil {
		t.Fatal(e)
	}
	if e := s.syncRankUser(id); e != nil {
		t.Fatal(e)
	}
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", id).Scan(&credits)
	if credits != 10 || rankBalance(t, s, id) != 1667 {
		t.Fatal(credits)
	}
	s.db.Exec("UPDATE users SET points=700 WHERE id=?", id)
	s.now = func() time.Time { return start.AddDate(0, 0, 400) }
	if e := s.syncRankUser(id); e != nil {
		t.Fatal(e)
	}
	u, e := userScan(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", id))
	if e != nil {
		t.Fatal(e)
	}
	s.decorate(u)
	if u.PaidTier != 0 || u.TierIndex != 3 || u.Credits != 10 || rankBalance(t, s, id) != 0 {
		t.Fatalf("expiry %+v", u)
	}
}

func TestRankUpgradeIsProratedAndIdempotent(t *testing.T) {
	s, _, id, cookie := billingFixture(t)
	start := s.now()
	o := billingPurchase(t, s, cookie, "rank_1_1", "alipay")
	if e := s.billingApply(billingSuccess(o), "base"); e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return start.Add(15 * 24 * time.Hour) }
	s.db.Exec("UPDATE sessions SET expires_at=? WHERE user_id=?", stamp(start.AddDate(0, 0, 30)), id)
	quote, e := s.rankUpgradePlan(id, 7)
	if e != nil {
		t.Fatal(e)
	}
	if quote.AmountCents != 900 {
		t.Fatal(quote.AmountCents)
	}
	// Insert through the real checkout so the quote snapshot is preserved.
	up := billingPurchase(t, s, cookie, "rank_upgrade_7", "alipay")
	for i := 0; i < 2; i++ {
		if e := s.billingApply(billingSuccess(up), "upgrade"); e != nil {
			t.Fatal(e)
		}
	}
	var credits, tier int
	s.db.QueryRow("SELECT credits,paid_tier FROM users WHERE id=?", id).Scan(&credits, &tier)
	if credits != 10 || tier != 7 || rankBalance(t, s, id) != 6667 {
		t.Fatal(credits, tier)
	}
	s.now = func() time.Time { return start.Add(31 * 24 * time.Hour) }
	if e := s.syncRankUser(id); e != nil {
		t.Fatal(e)
	}
	if rankBalance(t, s, id) != 5000 {
		t.Fatal("upgrade extended the original batch or lost the new supplement")
	}
	s.now = func() time.Time { return start.Add(45 * 24 * time.Hour) }
	if rankBalance(t, s, id) != 0 {
		t.Fatal("upgrade supplement survived its 30-day expiry")
	}
}

func TestRankCataloguePricesAndHistoricalTopupOnce(t *testing.T) {
	plans := newBillingPlans()
	if len(plans) != 31 {
		t.Fatal(len(plans))
	}
	for _, p := range plans {
		if p.Type == "rank" {
			if p.RankTier < 1 || p.RankTier > 7 || p.AmountCents != rankPrice(p.RankTier, p.Months) {
				t.Fatal(p)
			}
		}
	}
	s, _, id, cookie := billingFixture(t)
	old := historicalBillingOrder(t, s, cookie, "credits_100", "alipay")
	if _, e := s.db.Exec("UPDATE billing_orders SET credits=100 WHERE id=?", old.ID); e != nil {
		t.Fatal(e)
	}
	old.Credits = 100
	old.Days = 0
	for i := 0; i < 2; i++ {
		if e := s.billingApply(billingSuccess(old), "old"); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.topupOldPurchases(); e != nil {
		t.Fatal(e)
	}
	var credits int
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", id).Scan(&credits)
	if credits != 1010 {
		t.Fatal(credits)
	}
}
