package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type billingProvider interface {
	CreatePayment(string, string, int64, string, time.Time) (huifuPaymentResult, error)
	QueryPayment(string, int64) (huifuPaymentResult, error)
	VerifyNotification(string, string) (huifuPaymentResult, error)
}
type billingService struct {
	provider       billingProvider
	merchantID     string
	disabledReason string
	createMu       sync.Mutex
}
type billingPlan struct {
	RankTier       int    `json:"rank_tier"`
	Months         int    `json:"months"`
	MonthlyCredits int    `json:"monthly_credits"`
	RankSnapshot   string `json:"-"`
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	AmountCents    int64  `json:"amount_cents"`
	Days           int    `json:"days"`
	Credits        int    `json:"credits"`
}

var centTestPlan = billingPlan{ID: "payment_test", Name: "1分钱真实支付测试", Type: "payment_test", AmountCents: 1}

var billingPlans = newBillingPlans()

type billingOrder struct {
	RankTier        int    `json:"rank_tier"`
	Months          int    `json:"months"`
	MonthlyCredits  int    `json:"monthly_credits"`
	RankSnapshot    string `json:"-"`
	ID              string `json:"id"`
	UserID          int64  `json:"-"`
	Plan            string `json:"plan"`
	Name            string `json:"name"`
	Type            string `json:"type"`
	AmountCents     int64  `json:"amount_cents"`
	Days            int    `json:"days"`
	Credits         int    `json:"credits"`
	Channel         string `json:"channel"`
	Status          string `json:"status"`
	PayURL          string `json:"pay_url"`
	QRPayload       string `json:"qr_payload"`
	PaymentKind     string `json:"payment_kind"`
	ExpiresAt       string `json:"expires_at"`
	PaidAt          string `json:"paid_at"`
	CreatedAt       string `json:"created_at"`
	MembershipUntil string `json:"membership_until"`
	PlatformTradeNo string `json:"-"`
	LastCheckedAt   string `json:"-"`
}

const billingSchema = `
CREATE TABLE IF NOT EXISTS billing_orders (
 id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id),plan TEXT NOT NULL,name TEXT NOT NULL,
 amount_cents INTEGER NOT NULL CHECK(amount_cents>0),days INTEGER NOT NULL CHECK(days>0),credits INTEGER NOT NULL DEFAULT 0 CHECK(credits>=0),channel TEXT NOT NULL CHECK(channel IN ('alipay','wechat')),
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','success','failed','expired')),
 pay_payload TEXT NOT NULL DEFAULT '',platform_trade_no TEXT NOT NULL DEFAULT '',expires_at TEXT NOT NULL,paid_at TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,updated_at TEXT NOT NULL,last_checked_at TEXT NOT NULL DEFAULT '',membership_until TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS billing_trade_unique ON billing_orders(platform_trade_no) WHERE platform_trade_no!='';
CREATE INDEX IF NOT EXISTS billing_user_orders ON billing_orders(user_id,created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS billing_pending_unique ON billing_orders(user_id,plan,channel) WHERE status='pending';
CREATE TABLE IF NOT EXISTS billing_events (
 event_key TEXT PRIMARY KEY,order_id TEXT NOT NULL REFERENCES billing_orders(id),status TEXT NOT NULL,created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS billing_grants (
 order_id TEXT PRIMARY KEY REFERENCES billing_orders(id),user_id INTEGER NOT NULL REFERENCES users(id),starts_at TEXT NOT NULL,ends_at TEXT NOT NULL,created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS billing_credit_grants (
 order_id TEXT PRIMARY KEY REFERENCES billing_orders(id),user_id INTEGER NOT NULL REFERENCES users(id),
 credits INTEGER NOT NULL CHECK(credits>0),ledger_id INTEGER NOT NULL UNIQUE REFERENCES ledger(id),created_at TEXT NOT NULL
);`

// InitBilling never contacts the payment gateway. Incomplete configuration disables checkout.
func InitBilling(s *server) error {
	if _, err := s.db.Exec(billingSchema); err != nil {
		return err
	}
	if err := migrateBillingCredits(s.db); err != nil {
		return err
	}
	if err := migrateRankBilling(s.db); err != nil {
		return err
	}
	if err := s.topupOldPurchases(); err != nil {
		return err
	}
	b := &billingService{disabledReason: "支付暂未开通，请稍后再试"}
	s.billing = b
	if strings.TrimSpace(os.Getenv("PAY_PROVIDER")) != "huifu" {
		return nil
	}
	c := newHuifuPayClientFromEnv()
	if c.validateConfiguration() != nil {
		b.disabledReason = "支付配置尚未完成，暂不能购买"
		return nil
	}
	if s.origin == "" || c.notifyURL != strings.TrimRight(s.origin, "/")+"/api/billing/notify/huifu" {
		b.disabledReason = "支付通知地址尚未配置，暂不能购买"
		return nil
	}
	b.provider, b.merchantID, b.disabledReason = c, c.huifuID, ""
	return nil
}

func migrateBillingCredits(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(billing_orders)")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, required, primary int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &required, &defaultValue, &primary); err != nil {
			rows.Close()
			return err
		}
		found = found || name == "credits"
	}
	err = rows.Err()
	rows.Close()
	if err != nil || found {
		return err
	}
	_, err = db.Exec("ALTER TABLE billing_orders ADD COLUMN credits INTEGER NOT NULL DEFAULT 0 CHECK(credits>=0)")
	return err
}
func (s *server) billingRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/billing/plans", s.billingCatalog)
	m.HandleFunc("GET /api/billing/rank-status", s.rankStatus)
	m.HandleFunc("GET /api/billing/rank-upgrade", s.rankUpgradeQuote)
	m.HandleFunc("POST /api/billing/orders", s.billingCreate)
	m.HandleFunc("GET /api/billing/orders", s.billingList)
	m.HandleFunc("GET /api/billing/orders/{id}", s.billingStatus)
	m.HandleFunc("POST /api/billing/notify/huifu", s.billingNotify)
	m.HandleFunc("GET /api/billing/notify/huifu", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			billingAck(w, 405, "FAIL")
			return
		}
		billingAck(w, 200, "success")
	})
}
func (s *server) billingEnabled() bool { return s.billing != nil && s.billing.provider != nil }
func (s *server) billingReason() string {
	if s.billing != nil && s.billing.disabledReason != "" {
		return s.billing.disabledReason
	}
	return "支付暂未开通，请稍后再试"
}
func (s *server) billingCatalog(w http.ResponseWriter, r *http.Request) {
	reason := ""
	if !s.billingEnabled() {
		reason = s.billingReason()
	}
	send(w, 200, map[string]any{"enabled": s.billingEnabled(), "disabled_reason": reason, "plans": billingPlans, "channels": []string{"alipay"}, "auto_renew": false})
}

const billingOrderCols = "id,user_id,plan,name,amount_cents,days,credits,channel,status,pay_payload,platform_trade_no,expires_at,paid_at,created_at,membership_until,last_checked_at,rank_tier,months,monthly_credits,rank_snapshot"

func billingScan(row scanner) (*billingOrder, error) {
	o := &billingOrder{}
	err := row.Scan(&o.ID, &o.UserID, &o.Plan, &o.Name, &o.AmountCents, &o.Days, &o.Credits, &o.Channel, &o.Status, &o.QRPayload, &o.PlatformTradeNo, &o.ExpiresAt, &o.PaidAt, &o.CreatedAt, &o.MembershipUntil, &o.LastCheckedAt, &o.RankTier, &o.Months, &o.MonthlyCredits, &o.RankSnapshot)
	if err != nil {
		return nil, err
	}
	o.Type = "membership"
	if o.Credits > 0 {
		o.Type, o.Days = "credits", 0
	}
	if o.RankTier > 0 {
		o.Type = "rank"
		if strings.HasPrefix(o.Plan, "rank_upgrade_") {
			o.Type = "rank_upgrade"
		}
	}
	if o.Plan == centTestPlan.ID {
		o.Type, o.Days = "payment_test", 0
	}
	// Keep historical orders readable/reconcilable, but do not offer an old
	// WeChat checkout entry now that this site accepts only new Alipay payments.
	if o.Status != "pending" || o.Channel != "alipay" || !billingPayPayload(o.QRPayload) {
		o.QRPayload = ""
	}
	if o.QRPayload != "" {
		o.PaymentKind = "qr"
		if billingWebURL(o.QRPayload) {
			o.PayURL = o.QRPayload
			o.PaymentKind = "link"
			if o.Type == "payment_test" {
				o.PaymentKind = "qr"
			}
		}
	}
	return o, nil
}
func billingWebURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && !strings.ContainsAny(raw, "\r\n")
}
func billingPayPayload(raw string) bool {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\r\n") {
		return false
	}
	u, e := url.Parse(raw)
	if e != nil || u.User != nil {
		return false
	}
	if u.Scheme == "alipays" {
		return u.Host != ""
	}
	// Signed Huifu replies may use an HTTPS intermediary. Do not invent a host
	// allowlist, but never present an explicit WeChat payment URL as Alipay.
	if u.Scheme != "https" || !billingWebURL(raw) {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	for _, wechatDomain := range []string{"weixin.qq.com", "wx.qq.com", "tenpay.com"} {
		if host == wechatDomain || strings.HasSuffix(host, "."+wechatDomain) {
			return false
		}
	}
	return true
}
func (s *server) billingLoad(ctx context.Context, id string, userID int64) (*billingOrder, error) {
	return billingScan(s.db.QueryRowContext(ctx, "SELECT "+billingOrderCols+" FROM billing_orders WHERE id=? AND user_id=?", id, userID))
}
func newPaymentNumber(prefix string) string {
	return prefix + time.Now().UTC().Format("20060102") + "IG" + token(10)
}
func (s *server) billingCreate(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	var req struct {
		Plan    string `json:"plan"`
		Channel string `json:"channel"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Channel != "alipay" {
		fail(w, 400, "当前仅支持支付宝支付")
		return
	}
	if !s.billingEnabled() {
		fail(w, 503, s.billingReason())
		return
	}
	var plan *billingPlan
	for i := range billingPlans {
		if billingPlans[i].ID == req.Plan {
			plan = &billingPlans[i]
			break
		}
	}
	if req.Plan == centTestPlan.ID {
		if u.Role != "admin" || u.Email != founderEmail || u.TestMode {
			fail(w, 403, "仅站长管理员模式可生成真实支付测试订单")
			return
		}
		plan = &centTestPlan
	}
	if strings.HasPrefix(req.Plan, "rank_upgrade_") {
		target, e := strconv.Atoi(strings.TrimPrefix(req.Plan, "rank_upgrade_"))
		if e != nil {
			fail(w, 400, "请选择有效段位")
			return
		}
		plan, e = s.rankUpgradePlan(u.ID, target)
		if e != nil {
			fail(w, 409, e.Error())
			return
		}
	}
	if plan == nil {
		fail(w, 400, "请选择有效套餐")
		return
	}
	// Serialize only checkout creation so repeated clicks reuse the same pending invoice.
	s.billing.createMu.Lock()
	defer s.billing.createMu.Unlock()
	now := s.now()
	if _, err := s.db.ExecContext(r.Context(), "UPDATE billing_orders SET status='expired',updated_at=? WHERE user_id=? AND status='pending' AND expires_at<=?", stamp(now), u.ID, stamp(now)); err != nil {
		fail(w, 500, "订单暂时无法创建")
		return
	}
	existing, err := billingScan(s.db.QueryRowContext(r.Context(), "SELECT "+billingOrderCols+" FROM billing_orders WHERE user_id=? AND plan=? AND channel=? AND status='pending'", u.ID, plan.ID, req.Channel))
	if err == nil {
		send(w, 200, map[string]any{"order": existing, "reused": true})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		fail(w, 500, "订单暂时无法读取")
		return
	}
	// Bound accidental repeated checkout attempts without introducing paid mock orders.
	var count int
	if err = s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM billing_orders WHERE user_id=? AND created_at>?", u.ID, stamp(now.Add(-time.Hour))).Scan(&count); err != nil {
		fail(w, 500, "订单暂时无法创建")
		return
	}
	if count >= 10 {
		fail(w, 429, "创建订单过于频繁，请稍后再试")
		return
	}
	if plan.RankTier > 0 {
		var pending int
		if e := s.db.QueryRow("SELECT COUNT(*) FROM billing_orders WHERE user_id=? AND rank_tier>0 AND status='pending'", u.ID).Scan(&pending); e != nil {
			fail(w, 500, "订单状态读取失败")
			return
		}
		if pending > 0 {
			fail(w, 409, "已有待付款的段位订单，请先完成或等待其过期，避免重复购买")
			return
		}
	}
	id := newPaymentNumber("P")
	expires := now.Add(30 * time.Minute)
	storedDays := plan.Days
	if plan.Type == "credits" || plan.Type == "payment_test" {
		// Old databases require days>0. The saved plan and credit snapshot determine
		// the product; this placeholder never grants or exposes membership days.
		storedDays = 1
	}
	_, err = s.db.ExecContext(r.Context(), `INSERT INTO billing_orders(id,user_id,plan,name,amount_cents,days,credits,channel,expires_at,created_at,updated_at,rank_tier,months,monthly_credits,rank_snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, u.ID, plan.ID, plan.Name, plan.AmountCents, storedDays, plan.Credits, req.Channel, stamp(expires), stamp(now), stamp(now), plan.RankTier, plan.Months, plan.MonthlyCredits, plan.RankSnapshot)
	if err != nil {
		fail(w, 500, "订单暂时无法创建")
		return
	}
	result, providerErr := s.billing.provider.CreatePayment(id, "InfoGap "+plan.Name, plan.AmountCents, req.Channel, expires)
	message := ""
	if providerErr != nil || result.MerchantID != s.billing.merchantID || result.PaymentNo != id || (result.TradeType != "" && result.TradeType != "A_NATIVE") || !billingPayPayload(result.JumpURL) {
		// Keep uncertain upstream results pending: a callback/status query can still reconcile payment.
		message = "暂未取得支付入口，订单已保留；请稍后查询状态，勿重复付款"
	} else {
		_, err = s.db.ExecContext(r.Context(), "UPDATE billing_orders SET pay_payload=?,updated_at=? WHERE id=? AND status='pending'", result.JumpURL, stamp(s.now()), id)
		if err != nil {
			fail(w, 500, "订单已创建，请在订单记录中查询")
			return
		}
	}
	o, err := s.billingLoad(r.Context(), id, u.ID)
	if err != nil {
		fail(w, 500, "订单暂时无法读取")
		return
	}
	code := 201
	if message != "" {
		code = 202
	}
	send(w, code, map[string]any{"order": o, "message": message})
}
func (s *server) billingList(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	if s.billing == nil {
		send(w, 200, map[string]any{"items": []billingOrder{}})
		return
	}
	rows, err := s.db.QueryContext(r.Context(), "SELECT "+billingOrderCols+" FROM billing_orders WHERE user_id=? ORDER BY created_at DESC LIMIT 50", u.ID)
	if err != nil {
		fail(w, 500, "订单暂时无法读取")
		return
	}
	defer rows.Close()
	items := []*billingOrder{}
	for rows.Next() {
		o, e := billingScan(rows)
		if e != nil {
			fail(w, 500, "订单暂时无法读取")
			return
		}
		billingDisplayExpiry(o, s.now())
		items = append(items, o)
	}
	if rows.Err() != nil {
		fail(w, 500, "订单暂时无法读取")
		return
	}
	send(w, 200, map[string]any{"items": items})
}
func billingDisplayExpiry(o *billingOrder, now time.Time) {
	expiry, _ := time.Parse(time.RFC3339, o.ExpiresAt)
	if o.Status == "pending" && !expiry.After(now) {
		o.Status = "expired"
		o.PayURL = ""
		o.QRPayload = ""
		o.PaymentKind = ""
	}
}
func (s *server) billingStatus(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	o, err := s.billingLoad(r.Context(), r.PathValue("id"), u.ID)
	if err != nil {
		fail(w, 404, "订单不存在")
		return
	}
	message := ""
	if (o.Status == "pending" || o.Status == "expired") && s.billingEnabled() {
		now := s.now()
		cutoff := stamp(now.Add(-5 * time.Second))
		result, e := s.db.ExecContext(r.Context(), "UPDATE billing_orders SET last_checked_at=? WHERE id=? AND last_checked_at<=?", stamp(now), o.ID, cutoff)
		if e == nil {
			n, _ := result.RowsAffected()
			if n == 1 {
				check, e := s.billing.provider.QueryPayment(o.ID, o.AmountCents)
				if e != nil {
					message = "支付状态暂时无法核实，请稍后再查"
				} else if check.PaymentNo != o.ID {
					message = "支付信息核对未通过，请联系站长"
				} else if e = s.billingApply(check, "query:"+digest(check.ResponsePayload+check.PaymentNo+check.TransactionStatus)); e != nil {
					message = "支付信息核对未通过，请稍后再查"
				}
			}
		}
	}
	o, err = s.billingLoad(r.Context(), o.ID, u.ID)
	if err != nil {
		fail(w, 500, "订单暂时无法读取")
		return
	}
	billingDisplayExpiry(o, s.now())
	refreshed, e := userScan(s.db.QueryRowContext(r.Context(), "SELECT "+userCols+" FROM users WHERE id=?", u.ID))
	if e == nil {
		u = s.decorate(refreshed)
	}
	send(w, 200, map[string]any{"order": o, "user": u, "message": message})
}
func (s *server) billingNotify(w http.ResponseWriter, r *http.Request) {
	if !s.billingEnabled() {
		billingAck(w, 503, "FAIL")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		billingAck(w, 400, "FAIL")
		return
	}
	// A gateway configuration probe is acknowledged without performing settlement.
	if len(body) == 0 && r.URL.RawQuery == "" {
		billingAck(w, 200, "success")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		billingAck(w, 415, "FAIL")
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil || r.URL.RawQuery != "" || len(form["sign"]) != 1 || len(form["resp_data"]) != 1 {
		billingAck(w, 400, "FAIL")
		return
	}
	raw, sig := form.Get("resp_data"), form.Get("sign")
	result, err := s.billing.provider.VerifyNotification(raw, sig)
	if err != nil || (result.TransactionStatus != "S" && result.TransactionStatus != "F") {
		log.Printf("payment_notify_rejected stage=verification error=%v", err)
		billingAck(w, 400, "FAIL")
		return
	}
	if err = s.billingApply(result, "notify:"+digest(raw)); err != nil {
		log.Printf("payment_notify_rejected stage=settlement order=%s error=%v", result.PaymentNo, err)
		billingAck(w, 500, "FAIL")
		return
	}
	billingAck(w, 200, "RECV_ORD_ID_"+result.PaymentNo)
}
func billingAck(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(value))
}

// billingApply is the sole entitlement writer. Authentication at the transport boundary
// is mandatory; merchant/order/amount/state checks are repeated here before the transaction.
func (s *server) billingApply(result huifuPaymentResult, eventKey string) error {
	if s.billing == nil || result.MerchantID != s.billing.merchantID || result.PaymentNo == "" || result.AmountCents <= 0 {
		return errors.New("payment identity mismatch")
	}
	if result.TransactionStatus != "S" && result.TransactionStatus != "F" && result.TransactionStatus != "P" {
		return errors.New("unknown payment status")
	}
	if result.TransactionStatus == "S" && (result.OrderStatus != "1" || result.PlatformTradeNo == "") {
		return errors.New("incomplete payment success")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, err := billingScan(tx.QueryRow("SELECT "+billingOrderCols+" FROM billing_orders WHERE id=?", result.PaymentNo))
	if err != nil {
		return err
	}
	if o.AmountCents != result.AmountCents {
		return errors.New("payment amount mismatch")
	}
	// Native channel declarations, when present in a verified reply, must match
	// the stored invoice. Older replies may omit this optional field entirely.
	if (result.TradeType == "A_NATIVE" && o.Channel != "alipay") || (result.TradeType == "T_NATIVE" && o.Channel != "wechat") {
		return errors.New("payment channel mismatch")
	}
	if o.Status == "success" {
		if result.TransactionStatus == "S" && result.PlatformTradeNo != o.PlatformTradeNo {
			return errors.New("payment transaction mismatch")
		}
		return tx.Commit()
	}
	if result.TransactionStatus == "P" {
		return tx.Commit()
	}
	if o.Status == "failed" {
		if result.TransactionStatus == "F" {
			return tx.Commit()
		}
		return errors.New("payment already failed")
	}
	if o.Status != "pending" && o.Status != "expired" {
		return errors.New("payment state mismatch")
	}
	now := s.now()
	if result.TransactionStatus == "F" {
		_, err = tx.Exec("UPDATE billing_orders SET status='failed',updated_at=? WHERE id=?", stamp(now), o.ID)
	} else if o.Plan == centTestPlan.ID {
		if o.AmountCents != 1 || o.Credits != 0 {
			return errors.New("invalid test payment snapshot")
		}
		_, err = tx.Exec("UPDATE billing_orders SET status='success',platform_trade_no=?,paid_at=?,updated_at=? WHERE id=?", result.PlatformTradeNo, stamp(now), stamp(now), o.ID)
	} else if o.RankTier > 0 {
		if _, err = tx.Exec("UPDATE billing_orders SET status='success',platform_trade_no=?,paid_at=?,updated_at=? WHERE id=?", result.PlatformTradeNo, stamp(now), stamp(now), o.ID); err != nil {
			return err
		}
		err = applyRankOrder(tx, o, now)
	} else if o.Credits > 0 {
		// Price and quantity come from the stored order, never from callback/client input.
		// Both the platform trade and the order grant must be unique before any credit sticks.
		if _, err = tx.Exec("UPDATE billing_orders SET status='success',platform_trade_no=?,paid_at=?,updated_at=? WHERE id=?", result.PlatformTradeNo, stamp(now), stamp(now), o.ID); err != nil {
			return err
		}
		entry, e := tx.Exec("INSERT INTO ledger(user_id,kind,amount,created_at,note) VALUES(?,'purchase',?,?,?)", o.UserID, o.Credits, stamp(now), "购买阅读额度 · "+o.ID)
		if e != nil {
			return e
		}
		ledgerID, e := entry.LastInsertId()
		if e != nil {
			return e
		}
		if _, err = tx.Exec("INSERT INTO billing_credit_grants(order_id,user_id,credits,ledger_id,created_at) VALUES(?,?,?,?,?)", o.ID, o.UserID, o.Credits, ledgerID, stamp(now)); err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE users SET credits=credits+? WHERE id=?", o.Credits, o.UserID)
		if err == nil {
			err = topupHistoricalCredits(tx, o, now)
		}
	} else {
		var currentUntil string
		if err = tx.QueryRow("SELECT membership_until FROM users WHERE id=?", o.UserID).Scan(&currentUntil); err != nil {
			return err
		}
		start := now
		if old, e := time.Parse(time.RFC3339, currentUntil); e == nil && old.After(start) {
			start = old
		}
		end := start.Add(time.Duration(o.Days) * 24 * time.Hour)
		// UNIQUE platform transaction and order grant prevent a payment funding two grants.
		if _, err = tx.Exec("UPDATE billing_orders SET status='success',platform_trade_no=?,paid_at=?,membership_until=?,updated_at=? WHERE id=?", result.PlatformTradeNo, stamp(now), stamp(end), stamp(now), o.ID); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO billing_grants(order_id,user_id,starts_at,ends_at,created_at) VALUES(?,?,?,?,?)", o.ID, o.UserID, stamp(start), stamp(end), stamp(now)); err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE users SET membership_until=? WHERE id=?", stamp(end), o.UserID)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO billing_events(event_key,order_id,status,created_at) VALUES(?,?,?,?)", eventKey, o.ID, result.TransactionStatus, stamp(now)); err != nil {
		return err
	}
	return tx.Commit()
}
