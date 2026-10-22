package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var rankMonthlyPrices = []int64{0, 1000, 1300, 1600, 1900, 2200, 2500, 2800}
var rankYearlyPrices = []int64{0, 6800, 8800, 10800, 12800, 14800, 16800, 18800}
var rankMonthlyGifts = []int{0, 300, 600, 1200, 2500, 4000, 7000, 10000}

func rankGift(tier, months int) int {
	bonus := 0
	switch months {
	case 3:
		bonus = 5
	case 6:
		bonus = 10
	case 12:
		bonus = 20
	}
	return rankMonthlyGifts[tier] * (100 + bonus) / 100
}
func rankDays(months int) int {
	if months == 12 {
		return 365
	}
	return months * 30
}
func rankPrice(tier, months int) int64 {
	if months == 12 {
		return rankYearlyPrices[tier]
	}
	return rankMonthlyPrices[tier] * int64(months)
}
func newBillingPlans() []billingPlan {
	out := []billingPlan{{ID: "credits_100", Name: "1000永久阅读积分", Type: "credits", AmountCents: 990, Credits: 1000}, {ID: "credits_350", Name: "3500永久阅读积分", Type: "credits", AmountCents: 2900, Credits: 3500}, {ID: "credits_1000", Name: "10000永久阅读积分", Type: "credits", AmountCents: 7900, Credits: 10000}}
	for tier := 1; tier <= 7; tier++ {
		for _, months := range []int{1, 3, 6, 12} {
			out = append(out, billingPlan{ID: fmt.Sprintf("rank_%d_%d", tier, months), Name: fmt.Sprintf("%s · %d个月", tiers[tier].Name, months), Type: "rank", AmountCents: rankPrice(tier, months), Days: rankDays(months), RankTier: tier, Months: months, MonthlyCredits: rankGift(tier, months)})
		}
	}
	return out
}

const rankBillingSchema = `
CREATE TABLE IF NOT EXISTS rank_periods(id INTEGER PRIMARY KEY AUTOINCREMENT,order_id TEXT NOT NULL REFERENCES billing_orders(id),user_id INTEGER NOT NULL REFERENCES users(id),period_index INTEGER NOT NULL,tier INTEGER NOT NULL,months INTEGER NOT NULL,starts_at TEXT NOT NULL,ends_at TEXT NOT NULL,monthly_credits INTEGER NOT NULL,granted_credits INTEGER NOT NULL DEFAULT 0,price_cents INTEGER NOT NULL,updated_at TEXT NOT NULL,UNIQUE(order_id,period_index));
CREATE INDEX IF NOT EXISTS rank_periods_user_time ON rank_periods(user_id,starts_at,ends_at);
CREATE TABLE IF NOT EXISTS rank_gift_events(period_id INTEGER NOT NULL REFERENCES rank_periods(id),total_credits INTEGER NOT NULL,ledger_id INTEGER NOT NULL UNIQUE REFERENCES ledger(id),created_at TEXT NOT NULL,PRIMARY KEY(period_id,total_credits));
CREATE TABLE IF NOT EXISTS rank_upgrades(order_id TEXT NOT NULL REFERENCES billing_orders(id),period_id INTEGER NOT NULL REFERENCES rank_periods(id),old_tier INTEGER NOT NULL,new_tier INTEGER NOT NULL,old_credits INTEGER NOT NULL,new_credits INTEGER NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(order_id,period_id));
CREATE TABLE IF NOT EXISTS credit_package_topups(order_id TEXT PRIMARY KEY REFERENCES billing_orders(id),credits INTEGER NOT NULL,ledger_id INTEGER NOT NULL UNIQUE REFERENCES ledger(id),created_at TEXT NOT NULL);
`

func ensureColumn(db *sql.DB, table, name, decl string) error {
	rows, e := db.Query("PRAGMA table_info(" + table + ")")
	if e != nil {
		return e
	}
	found := false
	for rows.Next() {
		var cid, nn, pk int
		var n, t string
		var d any
		if e = rows.Scan(&cid, &n, &t, &nn, &d, &pk); e != nil {
			rows.Close()
			return e
		}
		if n == name {
			found = true
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil || found {
		return e
	}
	_, e = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + name + " " + decl)
	return e
}
func migratePaidRanks(db *sql.DB) error {
	for _, c := range []struct{ t, n, d string }{{"users", "paid_tier", "INTEGER NOT NULL DEFAULT 0"}, {"users", "paid_until", "TEXT NOT NULL DEFAULT ''"}} {
		if e := ensureColumn(db, c.t, c.n, c.d); e != nil {
			return e
		}
	}
	return nil
}
func migrateRankBilling(db *sql.DB) error {
	for _, c := range []struct{ n, d string }{{"rank_tier", "INTEGER NOT NULL DEFAULT 0"}, {"months", "INTEGER NOT NULL DEFAULT 0"}, {"monthly_credits", "INTEGER NOT NULL DEFAULT 0"}, {"rank_snapshot", "TEXT NOT NULL DEFAULT ''"}} {
		if e := ensureColumn(db, "billing_orders", c.n, c.d); e != nil {
			return e
		}
	}
	_, e := db.Exec(rankBillingSchema)
	return e
}
func grantRankGifts(tx *sql.Tx, user int64, now time.Time) error {
	type gift struct {
		id             int64
		total, granted int
	}
	rows, e := tx.Query("SELECT id,monthly_credits,granted_credits FROM rank_periods WHERE user_id=? AND starts_at<=? AND monthly_credits>granted_credits ORDER BY starts_at,id", user, stamp(now))
	if e != nil {
		return e
	}
	items := []gift{}
	for rows.Next() {
		var g gift
		if e = rows.Scan(&g.id, &g.total, &g.granted); e != nil {
			rows.Close()
			return e
		}
		items = append(items, g)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, g := range items {
		delta := g.total - g.granted
		result, e := tx.Exec("INSERT INTO ledger(user_id,kind,amount,created_at,note) VALUES(?,'rank_monthly_gift',?,?,'段位月赠阅读积分 · 永久有效')", user, delta, stamp(now))
		if e != nil {
			return e
		}
		id, e := result.LastInsertId()
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO rank_gift_events(period_id,total_credits,ledger_id,created_at) VALUES(?,?,?,?)", g.id, g.total, id, stamp(now)); e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE users SET credits=credits+? WHERE id=?", delta, user); e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE rank_periods SET granted_credits=?,updated_at=? WHERE id=? AND granted_credits=?", g.total, stamp(now), g.id, g.granted); e != nil {
			return e
		}
	}
	var tier int
	var until string
	e = tx.QueryRow("SELECT tier,ends_at FROM rank_periods WHERE user_id=? AND starts_at<=? AND ends_at>? ORDER BY tier DESC,ends_at DESC LIMIT 1", user, stamp(now), stamp(now)).Scan(&tier, &until)
	if e == sql.ErrNoRows {
		tier = 0
		until = ""
	} else if e != nil {
		return e
	}
	if tier > 0 {
		next, e := tx.Query("SELECT tier,starts_at,ends_at FROM rank_periods WHERE user_id=? AND ends_at>? ORDER BY starts_at,id", user, stamp(now))
		if e != nil {
			return e
		}
		for next.Next() {
			var t int
			var st, en string
			if e = next.Scan(&t, &st, &en); e != nil {
				next.Close()
				return e
			}
			if st > until || t < tier {
				break
			}
			if en > until {
				until = en
			}
		}
		next.Close()
	}
	_, e = tx.Exec("UPDATE users SET paid_tier=?,paid_until=? WHERE id=?", tier, until, user)
	return e
}
func (s *server) syncRankUser(id int64) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = grantRankGifts(tx, id, s.now()); e != nil {
		return e
	}
	return tx.Commit()
}
func topupHistoricalCredits(tx *sql.Tx, o *billingOrder, now time.Time) error {
	old := map[string]int{"credits_100": 100, "credits_350": 350, "credits_1000": 1000}
	if old[o.Plan] != o.Credits || o.Credits <= 0 {
		return nil
	}
	var existing int
	e := tx.QueryRow("SELECT COUNT(*) FROM credit_package_topups WHERE order_id=?", o.ID).Scan(&existing)
	if e != nil || existing > 0 {
		return e
	}
	bonus := o.Credits * 9
	result, e := tx.Exec("INSERT INTO ledger(user_id,kind,amount,created_at,note) VALUES(?,'purchase_topup',?,?,'旧积分包升级补差 · 不重复发放')", o.UserID, bonus, stamp(now))
	if e != nil {
		return e
	}
	id, e := result.LastInsertId()
	if e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO credit_package_topups(order_id,credits,ledger_id,created_at) VALUES(?,?,?,?)", o.ID, bonus, id, stamp(now)); e != nil {
		return e
	}
	_, e = tx.Exec("UPDATE users SET credits=credits+? WHERE id=?", bonus, o.UserID)
	return e
}
func (s *server) topupOldPurchases() error {
	rows, e := s.db.Query("SELECT " + billingOrderCols + " FROM billing_orders WHERE status='success' AND credits>0")
	if e != nil {
		return e
	}
	items := []*billingOrder{}
	for rows.Next() {
		o, e := billingScan(rows)
		if e != nil {
			rows.Close()
			return e
		}
		items = append(items, o)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, o := range items {
		if e = topupHistoricalCredits(tx, o, s.now()); e != nil {
			return e
		}
	}
	return tx.Commit()
}

type rankUpgradePeriod struct {
	ID         int64  `json:"id"`
	Tier       int    `json:"tier"`
	Credits    int    `json:"credits"`
	NewCredits int    `json:"new_credits"`
	NewPrice   int64  `json:"new_price"`
	UpdatedAt  string `json:"updated_at"`
}

func (s *server) rankUpgradePlan(user int64, tier int) (*billingPlan, error) {
	if tier < 1 || tier > 7 {
		return nil, errors.New("请选择有效升级段位")
	}
	rows, e := s.db.Query("SELECT id,tier,months,period_index,starts_at,ends_at,monthly_credits,price_cents,updated_at FROM rank_periods WHERE user_id=? AND ends_at>? ORDER BY starts_at,id", user, stamp(s.now()))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	parts := []rankUpgradePeriod{}
	var amount int64
	last := ""
	now := s.now()
	for rows.Next() {
		var p rankUpgradePeriod
		var months, index int
		var start, end string
		var oldprice int64
		if e = rows.Scan(&p.ID, &p.Tier, &months, &index, &start, &end, &p.Credits, &oldprice, &p.UpdatedAt); e != nil {
			return nil, e
		}
		if p.Tier >= tier {
			continue
		}
		st, _ := time.Parse(time.RFC3339, start)
		en, _ := time.Parse(time.RFC3339, end)
		fraction := 1.0
		if st.Before(now) {
			fraction = en.Sub(now).Seconds() / en.Sub(st).Seconds()
		}
		if fraction <= 0 {
			continue
		}
		newprice := rankPrice(tier, months) / int64(months)
		if index == months-1 {
			newprice = rankPrice(tier, months) - newprice*int64(months-1)
		}
		delta := newprice - oldprice
		if delta < 0 {
			delta = 0
		}
		amount += int64(math.Ceil(float64(delta) * fraction))
		p.NewPrice = newprice
		p.NewCredits = p.Credits + int(math.Floor(float64(rankGift(tier, months)-p.Credits)*fraction))
		parts = append(parts, p)
		if end > last {
			last = end
		}
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if len(parts) == 0 {
		return nil, errors.New("没有可升档的有效付费时段")
	}
	if amount < 1 {
		amount = 1
	}
	snapshot, _ := json.Marshal(parts)
	return &billingPlan{ID: fmt.Sprintf("rank_upgrade_%d", tier), Name: tiers[tier].Name + " · 剩余时段升档", Type: "rank_upgrade", AmountCents: amount, Days: 1, RankTier: tier, RankSnapshot: string(snapshot)}, nil
}
func applyRankOrder(tx *sql.Tx, o *billingOrder, now time.Time) error {
	if o.RankTier < 1 || o.RankTier > 7 {
		return errors.New("invalid rank snapshot")
	}
	if strings.HasPrefix(o.Plan, "rank_upgrade_") {
		var parts []rankUpgradePeriod
		if json.Unmarshal([]byte(o.RankSnapshot), &parts) != nil || len(parts) == 0 {
			return errors.New("invalid upgrade snapshot")
		}
		for _, p := range parts {
			var tier int
			var credit int
			var updated string
			if e := tx.QueryRow("SELECT tier,monthly_credits,updated_at FROM rank_periods WHERE id=? AND user_id=?", p.ID, o.UserID).Scan(&tier, &credit, &updated); e != nil {
				return e
			}
			if tier != p.Tier || credit != p.Credits {
				return errors.New("rank upgrade superseded; reconcile required")
			}
			if _, e := tx.Exec("INSERT INTO rank_upgrades(order_id,period_id,old_tier,new_tier,old_credits,new_credits,created_at) VALUES(?,?,?,?,?,?,?)", o.ID, p.ID, p.Tier, o.RankTier, p.Credits, p.NewCredits, stamp(now)); e != nil {
				return e
			}
			if _, e := tx.Exec("UPDATE rank_periods SET tier=?,monthly_credits=?,price_cents=?,updated_at=? WHERE id=?", o.RankTier, p.NewCredits, p.NewPrice, stamp(now), p.ID); e != nil {
				return e
			}
		}
	} else {
		if !hasInt([]int{1, 3, 6, 12}, o.Months) || o.Days != rankDays(o.Months) || o.MonthlyCredits <= 0 {
			return errors.New("invalid rank term snapshot")
		}
		start := now
		var last sql.NullString
		if e := tx.QueryRow("SELECT max(ends_at) FROM rank_periods WHERE user_id=?", o.UserID).Scan(&last); e != nil {
			return e
		}
		if last.Valid {
			t, _ := time.Parse(time.RFC3339, last.String)
			if t.After(start) {
				start = t
			}
		}
		for i := 0; i < o.Months; i++ {
			st := start.Add(time.Duration(i) * 30 * 24 * time.Hour)
			en := st.Add(30 * 24 * time.Hour)
			if i == o.Months-1 {
				en = start.Add(time.Duration(o.Days) * 24 * time.Hour)
			}
			price := o.AmountCents / int64(o.Months)
			if i == o.Months-1 {
				price = o.AmountCents - price*int64(o.Months-1)
			}
			if _, e := tx.Exec("INSERT INTO rank_periods(order_id,user_id,period_index,tier,months,starts_at,ends_at,monthly_credits,price_cents,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)", o.ID, o.UserID, i, o.RankTier, o.Months, stamp(st), stamp(en), o.MonthlyCredits, price, stamp(now)); e != nil {
				return e
			}
		}
	}
	return grantRankGifts(tx, o.UserID, now)
}
func hasInt(xs []int, n int) bool {
	for _, x := range xs {
		if x == n {
			return true
		}
	}
	return false
}
func (s *server) rankStatus(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	rows, e := s.db.Query("SELECT tier,starts_at,ends_at,monthly_credits,granted_credits FROM rank_periods WHERE user_id=? AND ends_at>? ORDER BY starts_at,id", u.ID, stamp(s.now()))
	if e != nil {
		fail(w, 500, "段位记录读取失败")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	next := ""
	for rows.Next() {
		var tier, gift, granted int
		var st, en string
		if e = rows.Scan(&tier, &st, &en, &gift, &granted); e != nil {
			fail(w, 500, "段位记录读取失败")
			return
		}
		items = append(items, map[string]any{"tier": tier, "starts_at": st, "ends_at": en, "monthly_credits": gift, "granted_credits": granted})
		if granted < gift && (next == "" || st < next) {
			next = st
		}
	}
	send(w, 200, map[string]any{"user": u, "periods": items, "next_gift_at": next})
}
func (s *server) rankUpgradeQuote(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	tier, e := strconv.Atoi(r.URL.Query().Get("tier"))
	if e != nil {
		fail(w, 400, "请选择目标段位")
		return
	}
	p, e := s.rankUpgradePlan(u.ID, tier)
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	send(w, 200, map[string]any{"plan": p})
}
func (s *server) runRankGifts(ctx context.Context) {
	run := func() {
		rows, e := s.db.Query("SELECT DISTINCT user_id FROM rank_periods WHERE starts_at<=? AND monthly_credits>granted_credits LIMIT 200", stamp(s.now()))
		if e != nil {
			return
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			_ = s.syncRankUser(id)
		}
	}
	run()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
