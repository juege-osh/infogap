package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const readingPolicyVersion = 1

type contributionDiscount struct {
	MinPoints int `json:"min_points"`
	Percent   int `json:"percent"`
}

var contributionDiscounts = []contributionDiscount{{100, 95}, {300, 92}, {700, 90}, {1400, 85}, {2500, 80}}

func contributionDiscountPercent(points int) int {
	percent := 100
	for _, tier := range contributionDiscounts {
		if points >= tier.MinPoints {
			percent = tier.Percent
		}
	}
	return percent
}

func pricedBillingPlan(plan billingPlan, points int) billingPlan {
	plan.ListAmountCents = plan.AmountCents
	plan.DiscountPercent = 100
	if plan.Type == "reading" {
		plan.DiscountPercent = contributionDiscountPercent(points)
		// Prices are integer cents, rounded only once after applying the discount.
		plan.AmountCents = (plan.ListAmountCents*int64(plan.DiscountPercent) + 50) / 100
	}
	return plan
}

const readingBillingSchema = `
CREATE TABLE IF NOT EXISTS reading_entitlements(
 order_id TEXT PRIMARY KEY REFERENCES billing_orders(id),user_id INTEGER NOT NULL REFERENCES users(id),
 plan TEXT NOT NULL,delay_hours INTEGER NOT NULL CHECK(delay_hours>=0),starts_at TEXT NOT NULL,ends_at TEXT NOT NULL,
 created_at TEXT NOT NULL,policy_version INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS reading_entitlements_user_time ON reading_entitlements(user_id,starts_at,ends_at);
CREATE TABLE IF NOT EXISTS test_reading_access(
 user_id INTEGER PRIMARY KEY REFERENCES users(id),delay_hours INTEGER NOT NULL,starts_at TEXT NOT NULL,ends_at TEXT NOT NULL
);
`

func migrateReadingBilling(db *sql.DB) error {
	for _, field := range []struct{ name, declaration string }{
		{"reading_policy_version", "INTEGER NOT NULL DEFAULT 0"},
		{"reading_delay_hours", "INTEGER NOT NULL DEFAULT 0"},
		{"list_amount_cents", "INTEGER NOT NULL DEFAULT 0"},
		{"discount_percent", "INTEGER NOT NULL DEFAULT 100"},
	} {
		if err := ensureColumn(db, "billing_orders", field.name, field.declaration); err != nil {
			return err
		}
	}
	_, err := db.Exec(readingBillingSchema)
	return err
}

// New purchases queue after existing reading periods. No previous paid time is
// shortened, and a different term's speed begins when its own period starts.
func applyReadingOrder(tx *sql.Tx, order *billingOrder, now time.Time) error {
	if order.ReadingPolicyVersion != readingPolicyVersion || !hasInt([]int{1, 3, 6, 12}, order.Months) || order.Days != rankDays(order.Months) || order.Plan != fmt.Sprintf("reading_%d", order.Months) || order.DelayHours != map[int]int{1: 8, 3: 4, 6: 2, 12: 0}[order.Months] || order.Credits != 0 || order.RankTier != 0 || order.MonthlyCredits != 0 || order.ListAmountCents <= 0 || !hasInt([]int{100, 95, 92, 90, 85, 80}, order.DiscountPercent) || order.AmountCents != (order.ListAmountCents*int64(order.DiscountPercent)+50)/100 {
		return errors.New("invalid reading plan snapshot")
	}
	start, err := nextPaidReadingStart(tx, order.UserID, now)
	if err != nil {
		return err
	}
	end := start.Add(time.Duration(order.Days) * 24 * time.Hour)
	if _, err := tx.Exec("INSERT INTO reading_entitlements(order_id,user_id,plan,delay_hours,starts_at,ends_at,created_at,policy_version) VALUES(?,?,?,?,?,?,?,?)", order.ID, order.UserID, order.Plan, order.DelayHours, stamp(start), stamp(end), stamp(now), readingPolicyVersion); err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE billing_orders SET membership_until=? WHERE id=?", stamp(end), order.ID)
	return err
}

func nextPaidReadingStart(tx *sql.Tx, user int64, now time.Time) (time.Time, error) {
	var latest sql.NullString
	if err := tx.QueryRow("SELECT MAX(ends_at) FROM (SELECT ends_at FROM reading_entitlements WHERE user_id=? UNION ALL SELECT p.ends_at FROM rank_periods p JOIN billing_orders o ON o.id=p.order_id WHERE p.user_id=? AND o.status='success' UNION ALL SELECT g.ends_at FROM billing_grants g JOIN billing_orders o ON o.id=g.order_id WHERE g.user_id=? AND o.status='success')", user, user, user).Scan(&latest); err != nil {
		return now, err
	}
	start := now
	if latest.Valid {
		end, err := time.Parse(time.RFC3339, latest.String)
		if err != nil {
			return now, err
		}
		if end.After(start) {
			start = end
		}
	}
	return start, nil
}

func setTestReading(tx *sql.Tx, user int64, months int, now time.Time) error {
	if !hasInt([]int{0, 1, 3, 6, 12}, months) {
		return errors.New("invalid test reading period")
	}
	var known bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM test_accounts WHERE user_id=?)", user).Scan(&known); err != nil {
		return err
	}
	if !known {
		return errors.New("not a test account")
	}
	if months == 0 {
		_, err := tx.Exec("DELETE FROM test_reading_access WHERE user_id=?", user)
		return err
	}
	delay := map[int]int{1: 8, 3: 4, 6: 2, 12: 0}[months]
	_, err := tx.Exec("INSERT INTO test_reading_access(user_id,delay_hours,starts_at,ends_at) VALUES(?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET delay_hours=excluded.delay_hours,starts_at=excluded.starts_at,ends_at=excluded.ends_at", user, delay, stamp(now), stamp(now.Add(time.Duration(rankDays(months))*24*time.Hour)))
	return err
}

// Legacy purchased rank periods are preserved. Invitation awards are deliberately
// excluded: their temporary rank cache is not proof of a paid reading entitlement.
func loadReadingAccess(q rankCreditReader, u *User, now time.Time) error {
	u.ReadingActive, u.ReadingUntil, u.ReadingDelayHours = false, "", 0
	rows, err := q.Query(`SELECT delay_hours,starts_at,ends_at FROM reading_entitlements WHERE user_id=? AND ends_at>?
UNION ALL SELECT CASE WHEN p.tier>=7 THEN 0 WHEN p.tier=6 THEN 1 WHEN p.tier=5 THEN 3 WHEN p.tier=4 THEN 6 ELSE 8 END,p.starts_at,p.ends_at
FROM rank_periods p JOIN billing_orders o ON o.id=p.order_id WHERE p.user_id=? AND p.ends_at>? AND o.status='success'
UNION ALL SELECT 8,g.starts_at,g.ends_at FROM billing_grants g JOIN billing_orders o ON o.id=g.order_id WHERE g.user_id=? AND g.ends_at>? AND o.status='success'
UNION ALL SELECT t.delay_hours,t.starts_at,t.ends_at FROM test_reading_access t JOIN test_accounts a ON a.user_id=t.user_id WHERE t.user_id=? AND t.ends_at>?
ORDER BY 2,3`, u.ID, stamp(now), u.ID, stamp(now), u.ID, stamp(now), u.ID, stamp(now))
	if err != nil {
		return err
	}
	defer rows.Close()
	type period struct {
		delay      int
		start, end string
	}
	periods := []period{}
	active, delay, until := false, 0, ""
	for rows.Next() {
		var p period
		if err := rows.Scan(&p.delay, &p.start, &p.end); err != nil {
			return err
		}
		periods = append(periods, p)
		if p.start <= stamp(now) && (!active || p.delay < delay) {
			active, delay = true, p.delay
		}
		if p.start <= stamp(now) && p.end > until {
			until = p.end
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if active {
		for _, p := range periods {
			if p.start <= until && p.end > until {
				until = p.end
			}
		}
	}
	u.ReadingActive, u.ReadingDelayHours, u.ReadingUntil = active, delay, until
	return nil
}

func (s *server) readingStatus(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	rows, err := s.db.Query("SELECT e.plan,o.name,e.delay_hours,e.starts_at,e.ends_at FROM reading_entitlements e JOIN billing_orders o ON o.id=e.order_id WHERE e.user_id=? AND e.ends_at>? ORDER BY e.starts_at", u.ID, stamp(s.now()))
	if err != nil {
		fail(w, 500, "畅读权益读取失败")
		return
	}
	defer rows.Close()
	periods := []map[string]any{}
	for rows.Next() {
		var plan, name, start, end string
		var delay int
		if err = rows.Scan(&plan, &name, &delay, &start, &end); err != nil {
			fail(w, 500, "畅读权益读取失败")
			return
		}
		periods = append(periods, map[string]any{"plan": plan, "name": name, "delay_hours": delay, "starts_at": start, "ends_at": end})
	}
	if rows.Err() != nil {
		fail(w, 500, "畅读权益读取失败")
		return
	}
	send(w, 200, map[string]any{"user": u, "periods": periods, "renewal_policy": "append"})
}
