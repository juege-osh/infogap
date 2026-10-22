package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const founderEmail = "1402272093@qq.com"
const version = "1.0.13"

var buildCommit = "dev"

var categories = []string{"前沿科技", "人工智能", "生命科学", "物理", "化学", "地球与地理", "天文与空间", "材料科学", "开源技术", "军事与国防", "政治与公共政策", "农业与食品", "社会民生", "法律法规", "航空航天", "经济与产业", "能源与环境", "医疗健康", "教育与就业", "交通与基建", "公共安全", "生活实用", "省钱优惠", "招聘求职", "职业成长", "住房居住", "出行旅行", "商业创业"}

type Tier struct {
	Name           string `json:"name"`
	MinPoints      int    `json:"min_points"`
	Color          string `json:"color"`
	DelayHours     int    `json:"delay_hours"`
	DailyAllowance int    `json:"daily_allowance"`
}

var tiers = []Tier{{"坚韧黑铁", 0, "#88909a", 72, 1}, {"英勇青铜", 100, "#b5896b", 48, 2}, {"荣耀白银", 300, "#c5ced6", 24, 3}, {"华贵黄金", 700, "#e8b84b", 12, 5}, {"璀璨铂金", 1400, "#75cacc", 6, 8}, {"流光翡翠", 2500, "#59c595", 3, 10}, {"璀璨钻石", 4000, "#80b3ed", 1, 15}, {"超凡大师", 6500, "#b994eb", 0, 20}, {"傲世宗师", 10000, "#eb8383", 0, 30}, {"最强王者", 15000, "#f5d575", 0, 50}}

type Entitlements struct {
	Subscriptions     bool `json:"subscriptions"`
	SubscriptionLimit int  `json:"subscription_limit"`
	DelayHours        int  `json:"delay_hours"`
}

type User struct {
	PaidTier              int          `json:"paid_tier"`
	PaidUntil             string       `json:"paid_until"`
	EarnedTierIndex       int          `json:"earned_tier_index"`
	EarnedTier            string       `json:"earned_tier"`
	TestMode              bool         `json:"test_mode"`
	CanTestMode           bool         `json:"can_test_mode"`
	ID                    int64        `json:"id"`
	MembershipUntil       string       `json:"membership_until"`
	IsMember              bool         `json:"is_member"`
	Entitlements          Entitlements `json:"entitlements"`
	Email                 string       `json:"email"`
	Username              string       `json:"username"`
	Role                  string       `json:"role"`
	Points                int          `json:"points"`
	Tier                  string       `json:"tier"`
	TierIndex             int          `json:"tier_index"`
	Credits               int          `json:"credits"`
	RankCredits           int          `json:"rank_credits"`
	RankCreditsExpiresAt  string       `json:"rank_credits_expires_at"`
	AvailableCredits      int          `json:"available_credits"`
	DailyRemaining        int          `json:"daily_remaining"`
	DailyAllowance        int          `json:"daily_allowance"`
	Contributions         int          `json:"contributions"`
	FeaturedContributions int          `json:"featured_contributions"`
	RareContributions     int          `json:"rare_contributions"`
	LastQualityAt         string       `json:"-"`
	Disabled              bool         `json:"disabled"`
	Week                  string       `json:"-"`
	WeeklyUsed            int          `json:"-"`
	Day                   string       `json:"-"`
	DailyUsed             int          `json:"-"`
}
type server struct {
	db                     *sql.DB
	billing                *billingService
	web, origin, otpSecret string
	secure                 bool
	mailer                 func(context.Context, string, string) error
	now                    func() time.Time
}
type scanner interface{ Scan(...any) error }

func userScan(r scanner) (*User, error) {
	u := &User{}
	err := r.Scan(&u.ID, &u.Email, &u.Username, &u.Role, &u.Points, &u.Credits, &u.Week, &u.WeeklyUsed, &u.Contributions, &u.FeaturedContributions, &u.RareContributions, &u.LastQualityAt, &u.Disabled, &u.MembershipUntil, &u.Day, &u.DailyUsed, &u.PaidTier, &u.PaidUntil)
	if err != nil {
		return nil, err
	}
	return u, nil
}

const userCols = "id,email,username,role,points,credits,week_key,weekly_used,contributions,featured_contributions,rare_contributions,last_quality_at,disabled,membership_until,day_key,daily_used,paid_tier,paid_until"

func (s *server) decorate(u *User) *User {
	if u == nil {
		return nil
	}
	u.CanTestMode = u.TestMode || (u.Role == "admin" && u.Email == founderEmail)
	u.TierIndex = 0
	for i, t := range tiers {
		if u.Points >= t.MinPoints {
			u.TierIndex = i
			u.Tier = t.Name
		}
	}
	u.EarnedTierIndex, u.EarnedTier = u.TierIndex, u.Tier
	paidUntil, _ := time.Parse(time.RFC3339, u.PaidUntil)
	if paidUntil.After(s.now()) && u.PaidTier > u.TierIndex && u.PaidTier < len(tiers) {
		u.TierIndex = u.PaidTier
		u.Tier = tiers[u.PaidTier].Name
	}
	until, _ := time.Parse(time.RFC3339, u.MembershipUntil)
	u.IsMember = until.After(s.now())
	limit := 0
	switch {
	case u.TierIndex >= 7:
		limit = 40
	case u.TierIndex >= 6:
		limit = 20
	case u.TierIndex >= 5:
		limit = 12
	case u.TierIndex >= 4:
		limit = 8
	case u.TierIndex >= 3:
		limit = 3
	case u.TierIndex >= 2:
		limit = 1
	}
	if u.IsMember {
		limit = max(limit, 20)
	}
	if u.Role == "admin" {
		limit = 40
	}
	delay := tiers[u.TierIndex].DelayHours
	if u.Role == "admin" || u.Role == "reviewer" {
		delay = 0
	}
	u.Entitlements = Entitlements{Subscriptions: limit > 0, SubscriptionLimit: limit, DelayHours: delay}
	u.DailyAllowance = tiers[u.TierIndex].DailyAllowance
	u.DailyRemaining = u.DailyAllowance
	if u.Day == dayKey(s.now()) {
		u.DailyRemaining = max(0, u.DailyAllowance-u.DailyUsed)
	}
	u.AvailableCredits = u.DailyRemaining + u.RankCredits + u.Credits
	return u
}
func weekKey(t time.Time) string {
	y, w := t.In(time.FixedZone("Asia/Shanghai", 8*3600)).ISOWeek()
	return fmt.Sprintf("%d-%02d", y, w)
}
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }
func token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func digest(x string) string { h := sha256.Sum256([]byte(x)); return hex.EncodeToString(h[:]) }
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	send(w, status, map[string]any{"error": msg, "request_id": w.Header().Get("X-Request-ID")})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		fail(w, 415, "请使用 JSON 请求")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "请求内容不正确")
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		fail(w, 400, "请求内容不正确")
		return false
	}
	return true
}
func idParam(r *http.Request) int64 { i, _ := strconv.ParseInt(r.PathValue("id"), 10, 64); return i }
func allowedURL(v string) bool {
	u, e := url.Parse(v)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && !strings.ContainsAny(v, "\r\n")
}
func (s *server) actor(r *http.Request) *User {
	c, e := r.Cookie("infogap_session")
	if e != nil || len(c.Value) != 64 {
		return nil
	}
	u, e := userScan(s.db.QueryRow(`SELECT `+prefixCols("u.", userCols)+` FROM users u JOIN sessions se ON se.user_id=u.id WHERE se.token_hash=? AND se.expires_at>? AND u.disabled=0`, digest(c.Value), stamp(s.now())))
	if e != nil {
		return nil
	}
	if s.billing != nil {
		if e = s.syncRankUser(u.ID); e != nil {
			return nil
		}
		u, e = userScan(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=? AND disabled=0", u.ID))
		if e != nil {
			return nil
		}
	}
	return s.decorateWithRank(u)
}
func (s *server) current(r *http.Request) *User {
	actor := s.actor(r)
	if actor == nil || !actor.CanTestMode {
		return actor
	}
	cookie, _ := r.Cookie("infogap_session")
	user, err := userScan(s.db.QueryRow("SELECT "+prefixCols("u.", userCols)+" FROM test_sessions ts JOIN test_accounts ta ON ta.user_id=ts.user_id JOIN users u ON u.id=ta.user_id WHERE ts.session_hash=? AND ta.owner_id=? AND u.role='member' AND u.disabled=0", digest(cookie.Value), actor.ID))
	if err == sql.ErrNoRows {
		return actor
	}
	if err != nil {
		return nil
	}
	s.decorateWithRank(user)
	user.TestMode, user.CanTestMode = true, true
	return user
}
func prefixCols(p, s string) string { return p + strings.ReplaceAll(s, ",", ","+p) }
func (s *server) require(w http.ResponseWriter, r *http.Request) *User {
	u := s.current(r)
	if u == nil {
		fail(w, 401, "请先通过邀请登录")
	}
	return u
}
func (s *server) staff(w http.ResponseWriter, r *http.Request, admin bool) *User {
	u := s.require(w, r)
	if u == nil {
		return nil
	}
	if u.Role != "admin" && (admin || u.Role != "reviewer") {
		fail(w, 403, "没有操作权限")
		return nil
	}
	return u
}
func (s *server) stats(u *User) map[string]int {
	m := map[string]int{"published": 0, "sources": 0, "members": 0, "pending": 0}
	visible, args := s.visibilityClause(u)
	n := 0
	_ = s.db.QueryRow("SELECT COUNT(*) FROM gaps g WHERE g.status='published'"+visible, args...).Scan(&n)
	m["published"] = n
	for key, q := range map[string]string{"sources": "SELECT COUNT(*) FROM sources WHERE enabled=1", "members": "SELECT COUNT(*) FROM users WHERE disabled=0 AND id NOT IN (SELECT user_id FROM test_accounts)"} {
		n = 0
		_ = s.db.QueryRow(q).Scan(&n)
		m[key] = n
	}
	if u != nil {
		n = 0
		if u.Role == "admin" || u.Role == "reviewer" {
			_ = s.db.QueryRow("SELECT COUNT(*) FROM gaps WHERE status='pending'").Scan(&n)
		} else {
			_ = s.db.QueryRow("SELECT COUNT(*) FROM gaps WHERE status='pending' AND author_id=?", u.ID).Scan(&n)
		}
		m["pending"] = n
	}
	return m
}
func (s *server) categoryCounts(u *User) map[string]int {
	counts := map[string]int{}
	for _, c := range categories {
		counts[c] = 0
	}
	visible, args := s.visibilityClause(u)
	rows, err := s.db.Query("SELECT g.category,COUNT(*) FROM gaps g WHERE g.status='published'"+visible+" GROUP BY g.category", args...)
	if err != nil {
		return counts
	}
	defer rows.Close()
	for rows.Next() {
		var category string
		var n int
		if rows.Scan(&category, &n) == nil {
			counts[category] = n
		}
	}
	return counts
}
func (s *server) bootstrap(w http.ResponseWriter, r *http.Request) {
	_, err := smtpSettings()
	u := s.current(r)
	send(w, 200, map[string]any{"user": u, "stats": s.stats(u), "categories": categories, "category_counts": s.categoryCounts(u), "topic_codes": topicCodes, "travel_topics": travelTopics, "travel_regions": travelRegions, "tiers": tiers, "rules": map[string]any{"welcome": 0, "contribution": 100, "contribution_credits": map[string]int{"ordinary": 100, "featured": 200, "rare": 400, "exclusive": 800}, "unlock_costs": unlockCosts, "daily_reset_timezone": "Asia/Shanghai"}, "mail_ready": err == nil})
}
func (s *server) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.db.PingContext(r.Context()); err != nil {
			fail(w, 503, "服务暂不可用")
			return
		}
		send(w, 200, map[string]any{"ok": true, "version": version, "commit": buildCommit, "service": "infogap"})
	})
	m.HandleFunc("GET /api/bootstrap", s.bootstrap)
	m.HandleFunc("GET /api/leaderboard", s.leaderboard)
	m.HandleFunc("POST /api/auth/code", s.authCode)
	m.HandleFunc("POST /api/auth/verify", s.authVerify)
	m.HandleFunc("POST /api/auth/logout", s.logout)
	m.HandleFunc("GET /api/gaps", s.listGaps)
	m.HandleFunc("GET /api/travel/facets", s.travelFacets)
	m.HandleFunc("GET /api/gaps/{id}", s.getGap)
	m.HandleFunc("POST /api/gaps/{id}/unlock", s.unlock)
	m.HandleFunc("POST /api/gaps/{id}/save", s.saveGap)
	m.HandleFunc("POST /api/gaps/{id}/report", s.reportGap)
	m.HandleFunc("POST /api/submissions", s.submit)
	m.HandleFunc("GET /api/me/activity", s.activity)
	m.HandleFunc("GET /api/subscriptions", s.listSubscriptions)
	m.HandleFunc("POST /api/subscriptions", s.addSubscription)
	m.HandleFunc("DELETE /api/subscriptions/{id}", s.deleteSubscription)
	m.HandleFunc("GET /api/admin/overview", s.overview)
	m.HandleFunc("POST /api/admin/invites", s.newInvite)
	m.HandleFunc("GET /api/admin/invites/{id}/campaign", s.getInviteCampaign)
	m.HandleFunc("POST /api/admin/invites/{id}/revoke", s.revokeInvite)
	m.HandleFunc("POST /api/admin/review/{id}", s.review)
	m.HandleFunc("POST /api/admin/gaps/{id}", s.editGap)
	m.HandleFunc("POST /api/admin/users/{id}", s.manageUser)
	m.HandleFunc("POST /api/admin/sources/{id}", s.manageSource)
	s.billingRoutes(m)
	s.feedbackRoutes(m)
	m.HandleFunc("POST /api/admin/test-mode", s.setTestMode)
	m.HandleFunc("POST /api/admin/diagnostics", s.diagnostics)
	m.HandleFunc("DELETE /api/admin/test-mode", s.exitTestMode)
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "接口不存在") })
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			fail(w, 405, "不支持此操作")
			return
		}
		p := filepath.Clean(r.URL.Path)
		for _, part := range strings.Split(p, "/") {
			if strings.HasPrefix(part, ".") {
				http.NotFound(w, r)
				return
			}
		}
		f := filepath.Join(s.web, p)
		info, err := os.Stat(f)
		if err != nil || info.IsDir() {
			if filepath.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
			f = filepath.Join(s.web, "index.html")
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, f)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			response := &supportResponse{ResponseWriter: w, status: 200}
			w = response
			requestID := "RQ-" + token(8)
			w.Header().Set("X-Request-ID", requestID)
			started := time.Now()
			defer func() {
				if response.status >= 400 && s.origin != "http://diagnostic.invalid" {
					log.Printf("api_error request_id=%s method=%s route=%q status=%d duration_ms=%d", requestID, r.Method, r.Pattern, response.status, time.Since(started).Milliseconds())
				}
			}()
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		paymentCallback := r.Method == "POST" && r.URL.Path == "/api/billing/notify/huifu"
		if r.Method != "GET" && r.Method != "HEAD" && !paymentCallback {
			o := r.Header.Get("Origin")
			expected := s.origin
			if expected == "" {
				expected = "http://" + r.Host
			}
			if o != "" && o != expected {
				fail(w, 403, "请求来源不匹配")
				return
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				fail(w, 403, "不允许跨站请求")
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				fail(w, 415, "请使用 JSON 请求")
				return
			}
		}
		if r.Method == "POST" && (r.URL.Path == "/api/billing/orders" || r.URL.Path == "/api/submissions" || (strings.HasPrefix(r.URL.Path, "/api/gaps/") && strings.HasSuffix(r.URL.Path, "/report"))) {
			if u := s.current(r); u != nil && u.TestMode {
				fail(w, 403, "测试模式支持阅读、收藏和订阅；付款、正式投稿和反馈请先返回管理员模式")
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}
func newServer(dbPath, web, origin string) (*server, error) {
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=10000;` + schema); err != nil {
		db.Close()
		return nil, err
	}
	if err = migrateSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(inviteCampaignSchema); err != nil {
		db.Close()
		return nil, err
	}
	s := &server{db: db, web: web, origin: origin, secure: strings.HasPrefix(origin, "https://"), otpSecret: token(32), now: time.Now}
	if err = initFeedback(s.db); err != nil {
		db.Close()
		return nil, err
	}
	s.mailer = sendCodeMail
	if err = InitBilling(s); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func main() {
	submissionBridge := flag.Bool("submission-review-bridge", false, "run the constrained submission review bridge")
	addr := flag.String("addr", "127.0.0.1:18869", "listen address")
	db := flag.String("db", "state/infogap.sqlite", "database file")
	web := flag.String("web", "web", "static directory")
	flag.Parse()
	if *submissionBridge {
		s, err := newSubmissionReviewServer(*db, os.Getenv("INFOGAP_PUBLIC_URL"))
		if err != nil {
			log.Fatal(err)
		}
		defer s.db.Close()
		if err := runSubmissionReviewBridge(s, os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	s, err := newServer(*db, *web, os.Getenv("INFOGAP_PUBLIC_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer s.db.Close()
	h := &http.Server{Addr: *addr, Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go s.runPaymentReconciliation(ctx)
	go s.runRankGifts(ctx)
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = h.Shutdown(c)
	}()
	log.Printf("InfoGap %s listening on %s", version, *addr)
	if err = h.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
func remoteIP(r *http.Request) string {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		return r.RemoteAddr
	}
	if host == "127.0.0.1" || host == "::1" {
		if ip := r.Header.Get("X-Real-IP"); net.ParseIP(ip) != nil {
			return ip
		}
	}
	return host
}
