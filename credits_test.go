package main

import (
	"fmt"
	"testing"
	"time"
)

func TestDailyTierScheduleAndLocalMidnight(t *testing.T) {
	s := testServer(t)
	for i, amount := range []int{1, 2, 3, 5, 8, 10, 15, 20, 30, 50} {
		u := s.decorate(&User{Role: "member", Points: tiers[i].MinPoints, Day: dayKey(s.now()), DailyUsed: 1})
		if u.DailyAllowance != amount || u.DailyRemaining != amount-1 {
			t.Fatalf("tier %d: %+v", i, u)
		}
	}
	id, cookie := account(t, s, "midnight@example.org", "member", 3)
	before := time.Date(2026, 10, 21, 15, 59, 59, 0, time.UTC)
	s.now = func() time.Time { return before }
	_, _ = s.db.Exec("UPDATE sessions SET expires_at=?", stamp(before.Add(48*time.Hour)))
	first := gap(t, s, "featured", "published", nil)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", first), nil, cookie), 200)
	var credits, used int
	var day string
	_ = s.db.QueryRow("SELECT credits,daily_used,day_key FROM users WHERE id=?", id).Scan(&credits, &used, &day)
	if credits != 2 || used != 1 || day != "2026-10-21" {
		t.Fatalf("split before midnight %d %d %s", credits, used, day)
	}
	s.now = func() time.Time { return before.Add(time.Second) }
	second := gap(t, s, "ordinary", "published", nil)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", second), nil, cookie), 200)
	_ = s.db.QueryRow("SELECT credits,daily_used,day_key FROM users WHERE id=?", id).Scan(&credits, &used, &day)
	if credits != 2 || used != 1 || day != "2026-10-22" {
		t.Fatalf("reset %d %d %s", credits, used, day)
	}
	// A long absence grants only today's allowance, never the missed days.
	s.now = func() time.Time { return before.Add(10 * 24 * time.Hour) }
	u, _ := userScan(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", id))
	if s.decorate(u).DailyRemaining != 1 {
		t.Fatal("daily gifts accumulated")
	}
}

func TestLevelPricesAndInsufficientSplitIsAtomic(t *testing.T) {
	s := testServer(t)
	id, cookie := account(t, s, "pricing@example.org", "member", 30)
	_, _ = s.db.Exec("UPDATE users SET points=4000,featured_contributions=1,rare_contributions=1,last_quality_at=?,day_key=?,daily_used=15 WHERE id=?", stamp(s.now()), dayKey(s.now()), id)
	want := 30
	for _, level := range []string{"ordinary", "featured", "rare", "exclusive"} {
		g := gap(t, s, level, "published", nil)
		path := fmt.Sprintf("/api/gaps/%d/unlock", g)
		mustStatus(t, request(s, "POST", path, nil, cookie), 200)
		mustStatus(t, request(s, "POST", path, nil, cookie), 200)
		want -= unlockCosts[level]
		var credits int
		_ = s.db.QueryRow("SELECT credits FROM users WHERE id=?", id).Scan(&credits)
		if credits != want {
			t.Fatalf("%s charged wrong amount: %d want %d", level, credits, want)
		}
	}
	_, _ = s.db.Exec("UPDATE users SET credits=0,daily_used=14 WHERE id=?", id)
	g := gap(t, s, "featured", "published", nil)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, cookie), 402)
	var used, count int
	_ = s.db.QueryRow("SELECT daily_used FROM users WHERE id=?", id).Scan(&used)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM unlocks WHERE gap_id=?", g).Scan(&count)
	if used != 14 || count != 0 {
		t.Fatal("failed payment consumed daily gifts")
	}
}

func TestSplitRefundSameDayAndExpiredGift(t *testing.T) {
	for _, nextDay := range []bool{false, true} {
		t.Run(fmt.Sprint(nextDay), func(t *testing.T) {
			s := testServer(t)
			_, admin := account(t, s, founderEmail, "admin", 0)
			id, cookie := account(t, s, "refund@example.org", "member", 10)
			g := gap(t, s, "featured", "published", nil)
			mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", g), nil, cookie), 200)
			if nextDay {
				base := s.now()
				s.now = func() time.Time { return base.Add(24 * time.Hour) }
				_, _ = s.db.Exec("UPDATE sessions SET expires_at=?", stamp(s.now().Add(time.Hour)))
			}
			path := fmt.Sprintf("/api/admin/review/%d", g)
			input := map[string]string{"action": "dispute", "note": "原始官方资料已撤回并更正"}
			mustStatus(t, request(s, "POST", path, input, admin), 200)
			mustStatus(t, request(s, "POST", path, input, admin), 409)
			var stored, used, gifts, refunds int
			_ = s.db.QueryRow("SELECT credits,daily_used FROM users WHERE id=?", id).Scan(&stored, &used)
			_ = s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE kind='daily_refund'").Scan(&gifts)
			_ = s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE kind='refund'").Scan(&refunds)
			if stored != 10 || refunds != 1 {
				t.Fatalf("stored refund %d %d", stored, refunds)
			}
			if nextDay && (used != 1 || gifts != 0) {
				t.Fatal("expired gift was converted or restored")
			}
			if !nextDay && (used != 0 || gifts != 1) {
				t.Fatal("same-day gift not refunded")
			}
		})
	}
}

func TestDailyMigrationPreservesBalancesAndOldUnlocks(t *testing.T) {
	s := testServer(t)
	id, _ := account(t, s, "legacy@example.org", "member", 117)
	g := gap(t, s, "ordinary", "published", nil)
	_, _ = s.db.Exec("INSERT INTO unlocks(user_id,gap_id,cost,kind,created_at) VALUES(?,?,1,'credit',?)", id, g, stamp(s.now()))
	for _, column := range []struct{ table, name string }{{"users", "day_key"}, {"users", "daily_used"}, {"unlocks", "daily_cost"}, {"unlocks", "day_key"}} {
		if _, err := s.db.Exec("ALTER TABLE " + column.table + " DROP COLUMN " + column.name); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrateReadingCredits(s.db); err != nil {
			t.Fatal(err)
		}
	}
	var credits, cost, daily int
	_ = s.db.QueryRow("SELECT credits FROM users WHERE id=?", id).Scan(&credits)
	_ = s.db.QueryRow("SELECT cost,daily_cost FROM unlocks WHERE user_id=?", id).Scan(&cost, &daily)
	if credits != 117 || cost != 1 || daily != 0 {
		t.Fatal("migration rewrote history")
	}
}
