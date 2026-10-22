package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func rankBalance(t *testing.T, s *server, user int64) int {
	t.Helper()
	u := &User{ID: user}
	if err := loadRankCredits(s.db, u, s.now()); err != nil {
		t.Fatal(err)
	}
	return u.RankCredits
}

func TestRankGiftCatalogueFinalAnnualValuesAndRounding(t *testing.T) {
	want := [7][4]int{{1667, 1750, 1833, 2000}, {3333, 3500, 3667, 4000}, {5000, 5250, 5500, 6000}, {6667, 7000, 7333, 8000}, {8333, 8750, 9167, 10000}, {10000, 10500, 11000, 12000}, {11667, 12250, 12833, 14000}}
	for tier := 1; tier <= 7; tier++ {
		for index, months := range []int{1, 3, 6, 12} {
			s, _, _, cookie := billingFixture(t)
			o := billingPurchase(t, s, cookie, fmt.Sprintf("rank_%d_%d", tier, months), "alipay")
			if o.MonthlyCredits != want[tier-1][index] || o.AmountCents != rankPrice(tier, months) || o.Days != rankDays(months) {
				t.Fatalf("wrong snapshot %+v", o)
			}
		}
	}
}

func TestRankGiftScheduledExpiryAndNoLoginCatchup(t *testing.T) {
	s, _, user, cookie := billingFixture(t)
	start := s.now()
	o := billingPurchase(t, s, cookie, "rank_1_3", "alipay")
	for range 2 {
		if err := s.billingApply(billingSuccess(o), "quarter"); err != nil {
			t.Fatal(err)
		}
	}
	if rankBalance(t, s, user) != 1750 {
		t.Fatal("first gift missing")
	}
	s.now = func() time.Time { return start.Add(30*24*time.Hour - time.Second) }
	if rankBalance(t, s, user) != 1750 {
		t.Fatal("expired early")
	}
	s.now = func() time.Time { return start.Add(30 * 24 * time.Hour) }
	if rankBalance(t, s, user) != 0 {
		t.Fatal("expiry did not apply before cleanup")
	}
	// No visits/workers for over two months: only the current scheduled batch
	// survives; missed periods must not restart their 30-day windows.
	s.now = func() time.Time { return start.Add(61 * 24 * time.Hour) }
	for range 2 {
		if err := s.syncRankUser(user); err != nil {
			t.Fatal(err)
		}
	}
	if rankBalance(t, s, user) != 1750 {
		t.Fatal("missed gifts revived")
	}
	var permanent, events, expired int
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", user).Scan(&permanent)
	s.db.QueryRow("SELECT COUNT(*) FROM rank_gift_events WHERE policy_version=2").Scan(&events)
	s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE kind='rank_expire'").Scan(&expired)
	if permanent != 10 || events != 3 || expired != 2 {
		t.Fatal(permanent, events, expired)
	}
	if _, err := s.db.Exec("UPDATE sessions SET expires_at=? WHERE user_id=?", stamp(start.Add(100*24*time.Hour)), user); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/billing/rank-status", nil, cookie)
	mustStatus(t, w, 200)
	var result struct {
		User    User
		Batches []rankCreditBatch
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Batches) != 1 || result.User.RankCredits != 1750 || result.User.Credits != 10 || result.User.AvailableCredits != 1750+10+2 || result.User.RankCreditsExpiresAt != stamp(start.Add(90*24*time.Hour)) || result.Batches[0].PolicyVersion != 2 {
		t.Fatal(w.Body.String())
	}
}

func TestRankGiftThreeSourceSpendAndRefundPreservesExpiry(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			s, _, user, cookie := billingFixture(t)
			_, admin := account(t, s, founderEmail, "admin", 0)
			start := s.now()
			o := billingPurchase(t, s, cookie, "rank_6_1", "alipay")
			if err := s.billingApply(billingSuccess(o), "base"); err != nil {
				t.Fatal(err)
			}
			// Leave 2 daily points, a 2-point earlier batch and a 3-point later
			// batch, so an 8-point unlock needs 1 permanent point as well.
			if _, err := s.db.Exec("UPDATE users SET day_key=?,daily_used=13 WHERE id=?", dayKey(start), user); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("UPDATE rank_credit_batches SET amount=2,remaining=2 WHERE user_id=?", user); err != nil {
				t.Fatal(err)
			}
			var period int64
			s.db.QueryRow("SELECT id FROM rank_periods WHERE order_id=?", o.ID).Scan(&period)
			if _, err := s.db.Exec("INSERT INTO rank_credit_batches(period_id,user_id,entitlement_total,amount,remaining,granted_at,expires_at) VALUES(?,?,999999,3,3,?,?)", period, user, stamp(start), stamp(start.Add(31*24*time.Hour))); err != nil {
				t.Fatal(err)
			}
			id := gap(t, s, "exclusive", "published", nil)
			path := fmt.Sprintf("/api/gaps/%d/unlock", id)
			for range 2 {
				mustStatus(t, request(s, "POST", path, nil, cookie), 200)
			}
			var permanent, daily, rank, stored, entries int
			s.db.QueryRow("SELECT credits,daily_used FROM users WHERE id=?", user).Scan(&permanent, &daily)
			s.db.QueryRow("SELECT rank_cost,cost FROM unlocks WHERE user_id=? AND gap_id=?", user, id).Scan(&rank, &stored)
			s.db.QueryRow("SELECT COUNT(*) FROM rank_credit_spends WHERE user_id=?", user).Scan(&entries)
			if permanent != 9 || daily != 15 || rank != 5 || stored != 1 || entries != 2 || rankBalance(t, s, user) != 0 {
				t.Fatal(permanent, daily, rank, stored, entries)
			}
			if expired {
				s.now = func() time.Time { return start.Add(32 * 24 * time.Hour) }
				if _, err := s.db.Exec("UPDATE sessions SET expires_at=?", stamp(start.Add(100*24*time.Hour))); err != nil {
					t.Fatal(err)
				}
			}
			mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/review/%d", id), map[string]string{"action": "dispute", "note": "已核实原始材料不支持该结论"}, admin), 200)
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = refundReadingCredits(tx, id, s.now()); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			s.db.QueryRow("SELECT credits FROM users WHERE id=?", user).Scan(&permanent)
			want := 5
			if expired {
				want = 0
			}
			if permanent != 10 || rankBalance(t, s, user) != want {
				t.Fatal("refund converted expired gift to permanent", permanent, rankBalance(t, s, user))
			}
			var earliest string
			s.db.QueryRow("SELECT MIN(expires_at) FROM rank_credit_batches WHERE user_id=?", user).Scan(&earliest)
			if earliest != stamp(start.Add(30*24*time.Hour)) {
				t.Fatal("refund extended expiry")
			}
		})
	}
}

func TestRankGiftOldPaidAndPendingSnapshotsArePreserved(t *testing.T) {
	for _, paid := range []bool{false, true} {
		t.Run(fmt.Sprint(paid), func(t *testing.T) {
			s, provider, user, cookie := billingFixture(t)
			// Exercise a genuine old schema, not only rows in the new schema.
			for _, query := range []string{"DROP TABLE rank_credit_spends", "DROP TABLE rank_credit_batches", "ALTER TABLE rank_gift_events DROP COLUMN policy_version", "ALTER TABLE rank_gift_events DROP COLUMN expires_at", "ALTER TABLE ledger DROP COLUMN expires_at", "ALTER TABLE unlocks DROP COLUMN rank_cost"} {
				if _, err := s.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			start := s.now()
			at := stamp(start)
			orderID := "P20261021IGlegacyrank"
			status := "pending"
			if paid {
				status = "success"
			}
			if _, err := s.db.Exec("INSERT INTO billing_orders(id,user_id,plan,name,amount_cents,days,rank_tier,months,monthly_credits,channel,status,expires_at,created_at,updated_at) VALUES(?,?,'rank_1_3','旧青铜季度',3000,90,1,3,315,'alipay',?,?,?,?)", orderID, user, status, stamp(start.Add(time.Hour)), at, at); err != nil {
				t.Fatal(err)
			}
			if paid {
				// A pre-policy payment has already received its first permanent gift.
				for i := 0; i < 3; i++ {
					granted := 0
					if i == 0 {
						granted = 315
					}
					res, err := s.db.Exec("INSERT INTO rank_periods(order_id,user_id,period_index,tier,months,starts_at,ends_at,monthly_credits,granted_credits,price_cents,updated_at) VALUES(?,?,?,1,3,?,?,315,?,1000,?)", orderID, user, i, stamp(start.Add(time.Duration(i)*30*24*time.Hour)), stamp(start.Add(time.Duration(i+1)*30*24*time.Hour)), granted, at)
					if err != nil {
						t.Fatal(err)
					}
					if i == 0 {
						period, _ := res.LastInsertId()
						ledger, err := s.db.Exec("INSERT INTO ledger(user_id,kind,amount,created_at,note) VALUES(?,'rank_monthly_gift',315,?,'旧永久赠分')", user, at)
						if err != nil {
							t.Fatal(err)
						}
						ledgerID, _ := ledger.LastInsertId()
						if _, err = s.db.Exec("INSERT INTO rank_gift_events(period_id,total_credits,ledger_id,created_at) VALUES(?,315,?,?)", period, ledgerID, at); err != nil {
							t.Fatal(err)
						}
					}
				}
				if _, err := s.db.Exec("UPDATE users SET credits=credits+315 WHERE id=?", user); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := migrateRankBilling(s.db); err != nil {
					t.Fatal(err)
				}
				reused := billingPurchase(t, s, cookie, "rank_1_3", "alipay")
				if reused.ID != orderID || reused.MonthlyCredits != 315 || provider.creates != 0 {
					t.Fatal("pending snapshot rewritten")
				}
				if err := s.billingApply(billingSuccess(reused), "old-paid-now"); err != nil {
					t.Fatal(err)
				}
			}
			// Re-running migrations must neither reprice rows nor turn old balance
			// into expiring gifts; future periods still use the original amount.
			for range 2 {
				if err := migrateRankBilling(s.db); err != nil {
					t.Fatal(err)
				}
			}
			s.now = func() time.Time { return start.Add(31 * 24 * time.Hour) }
			for range 2 {
				if err := s.syncRankUser(user); err != nil {
					t.Fatal(err)
				}
			}
			var balance, snapshot, legacyEvents int
			s.db.QueryRow("SELECT credits FROM users WHERE id=?", user).Scan(&balance)
			s.db.QueryRow("SELECT monthly_credits FROM billing_orders WHERE id=?", orderID).Scan(&snapshot)
			s.db.QueryRow("SELECT COUNT(*) FROM rank_gift_events WHERE policy_version=1").Scan(&legacyEvents)
			want := 10
			if paid {
				want += 315
			}
			if balance != want || snapshot != 315 || rankBalance(t, s, user) != 315 || legacyEvents != map[bool]int{false: 0, true: 1}[paid] {
				t.Fatal(balance, snapshot, legacyEvents, rankBalance(t, s, user))
			}
		})
	}
}

func TestRankGiftEarliestExpiryAndConcurrentUnlock(t *testing.T) {
	s, _, user, cookie := billingFixture(t)
	o := billingPurchase(t, s, cookie, "rank_1_1", "alipay")
	if err := s.billingApply(billingSuccess(o), "first"); err != nil {
		t.Fatal(err)
	}
	var period, early int64
	s.db.QueryRow("SELECT period_id,id FROM rank_credit_batches WHERE user_id=?", user).Scan(&period, &early)
	if _, err := s.db.Exec("UPDATE rank_credit_batches SET amount=2,remaining=2 WHERE id=?", early); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO rank_credit_batches(period_id,user_id,entitlement_total,amount,remaining,granted_at,expires_at) VALUES(?,?,999999,3,3,?,?)", period, user, stamp(s.now()), stamp(s.now().Add(31*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE users SET day_key=?,daily_used=2 WHERE id=?", dayKey(s.now()), user); err != nil {
		t.Fatal(err)
	}
	id := gap(t, s, "featured", "published", nil)
	path := fmt.Sprintf("/api/gaps/%d/unlock", id)
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- request(s, "POST", path, nil, cookie).Code }()
	}
	wg.Wait()
	close(results)
	for status := range results {
		if status != 200 {
			t.Fatal(status)
		}
	}
	var earlier, later, permanent, spends int
	s.db.QueryRow("SELECT remaining FROM rank_credit_batches WHERE id=?", early).Scan(&earlier)
	s.db.QueryRow("SELECT remaining FROM rank_credit_batches WHERE user_id=? AND id<>?", user, early).Scan(&later)
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", user).Scan(&permanent)
	s.db.QueryRow("SELECT COUNT(*) FROM rank_credit_spends WHERE user_id=?", user).Scan(&spends)
	if earlier != 0 || later != 3 || permanent != 10 || spends != 1 {
		t.Fatal("wrong priority or repeated charge", earlier, later, permanent, spends)
	}
}

func TestRankGiftUnlockFailureRollsBackAllSources(t *testing.T) {
	s, _, user, cookie := billingFixture(t)
	o := billingPurchase(t, s, cookie, "rank_6_1", "alipay")
	if err := s.billingApply(billingSuccess(o), "rollback-gift"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE rank_credit_batches SET amount=2,remaining=2 WHERE user_id=?", user); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE users SET day_key=?,daily_used=13 WHERE id=?", dayKey(s.now()), user); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER reject_rank_spend BEFORE INSERT ON ledger WHEN NEW.kind='rank_unlock' BEGIN SELECT RAISE(ABORT,'test rollback'); END"); err != nil {
		t.Fatal(err)
	}
	id := gap(t, s, "exclusive", "published", nil)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", id), nil, cookie), 500)
	var permanent, daily, unlocks, spends int
	if err := s.db.QueryRow("SELECT credits,daily_used FROM users WHERE id=?", user).Scan(&permanent, &daily); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow("SELECT COUNT(*) FROM unlocks WHERE user_id=?", user).Scan(&unlocks)
	s.db.QueryRow("SELECT COUNT(*) FROM rank_credit_spends WHERE user_id=?", user).Scan(&spends)
	if permanent != 10 || daily != 13 || rankBalance(t, s, user) != 2 || unlocks != 0 || spends != 0 {
		t.Fatal("failed unlock leaked a partial debit", permanent, daily, rankBalance(t, s, user), unlocks, spends)
	}
}

func TestRankGiftLateUpgradeCallbackSeparatesScheduledBaseAndDelta(t *testing.T) {
	s, _, user, cookie := billingFixture(t)
	start := s.now()
	if _, err := s.db.Exec("UPDATE sessions SET expires_at=? WHERE user_id=?", stamp(start.Add(100*24*time.Hour)), user); err != nil {
		t.Fatal(err)
	}
	o := billingPurchase(t, s, cookie, "rank_1_3", "alipay")
	if err := s.billingApply(billingSuccess(o), "base-quarter"); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return start.Add(29 * 24 * time.Hour) }
	upgrade := billingPurchase(t, s, cookie, "rank_upgrade_7", "alipay")
	// No requests or gift worker occur at the next period boundary. The signed
	// payment callback itself must separate the scheduled base from its delta.
	s.now = func() time.Time { return start.Add(31 * 24 * time.Hour) }
	for range 2 {
		if err := s.billingApply(billingSuccess(upgrade), "late-upgrade"); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.db.Query("SELECT b.amount,b.granted_at,b.expires_at FROM rank_credit_batches b JOIN rank_periods p ON p.id=b.period_id WHERE p.user_id=? AND p.period_index=1 ORDER BY b.id", user)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type batch struct {
		amount        int
		grant, expiry string
	}
	got := []batch{}
	for rows.Next() {
		var b batch
		if err = rows.Scan(&b.amount, &b.grant, &b.expiry); err != nil {
			t.Fatal(err)
		}
		got = append(got, b)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []batch{{1750, stamp(start.Add(30 * 24 * time.Hour)), stamp(start.Add(60 * 24 * time.Hour))}, {10500, stamp(start.Add(31 * 24 * time.Hour)), stamp(start.Add(61 * 24 * time.Hour))}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatal("late callback merged different expiry policies", got, want)
	}
}
