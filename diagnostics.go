package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

type diagnosticCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// This provider is attached only to an unreachable in-memory diagnostic server.
// It cannot reach the real gateway or write to the production account database.
type diagnosticProvider struct{ orders map[string]huifuPaymentResult }

func (p *diagnosticProvider) CreatePayment(id, subject string, amount int64, channel string, expires time.Time) (huifuPaymentResult, error) {
	if channel != "alipay" {
		return huifuPaymentResult{}, errors.New("unsupported diagnostic channel")
	}
	r := huifuPaymentResult{MerchantID: "diagnostic", PaymentNo: id, AmountCents: amount, TradeType: "A_NATIVE", JumpURL: "alipays://platformapi/startapp?appId=diagnostic-only", PlatformTradeNo: "diagnostic-" + id, TransactionStatus: "S", OrderStatus: "1"}
	p.orders[id] = r
	return r, nil
}
func (p *diagnosticProvider) QueryPayment(id string, amount int64) (huifuPaymentResult, error) {
	r, ok := p.orders[id]
	if !ok {
		return r, errors.New("unknown diagnostic order")
	}
	return r, nil
}
func (p *diagnosticProvider) VerifyNotification(raw, signature string) (huifuPaymentResult, error) {
	return huifuPaymentResult{}, errors.New("diagnostic provider has no public callbacks")
}

func (s *server) diagnostics(w http.ResponseWriter, r *http.Request) {
	if s.testModeOwner(w, r) == nil {
		return
	}
	var in struct {
		Group string `json:"group"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !has([]string{"all", "service", "auth", "reading", "subscriptions", "payment"}, in.Group) {
		fail(w, 400, "请选择已有自检项目")
		return
	}
	started := time.Now()
	checks := []diagnosticCheck{}
	add := func(name string, ok bool, detail string) {
		status := "passed"
		if !ok {
			status = "failed"
		}
		checks = append(checks, diagnosticCheck{name, status, detail})
	}
	if in.Group == "all" || in.Group == "service" {
		var integrity string
		err := s.db.QueryRowContext(r.Context(), "PRAGMA quick_check").Scan(&integrity)
		add("生产数据库检查", err == nil && integrity == "ok", "检查当前数据库，不修改业务数据")
		_, mailErr := smtpSettings()
		status := "ready"
		detail := "邮件配置就绪；未发送真实邮件"
		if mailErr != nil {
			status = "unavailable"
			detail = "邮件配置不完整，请检查服务器邮件设置"
		}
		checks = append(checks, diagnosticCheck{"验证码邮件配置", status, detail})
		status = "ready"
		detail = "支付宝配置就绪；未联系网关或执行真实扣款"
		if !s.billingEnabled() {
			status = "unavailable"
			detail = "支付宝配置未就绪"
		}
		checks = append(checks, diagnosticCheck{"真实支付配置", status, detail})
	}
	if in.Group != "service" {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		test, err := newServer(":memory:", "web", "http://diagnostic.invalid")
		if err != nil {
			fail(w, 500, "无法建立隔离自检环境")
			return
		}
		defer test.db.Close()
		test.now = s.now
		test.billing = &billingService{provider: &diagnosticProvider{orders: map[string]huifuPaymentResult{}}, merchantID: "diagnostic"}
		var code string
		test.mailer = func(_ context.Context, _, value string) error { code = value; return nil }
		memberCookie, ownerCookie := token(32), token(32)
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO users(id,email,username,role,credits,created_at) VALUES(1,'member@example.invalid','自检成员','member',10,?)", []any{stamp(s.now())}},
			{"INSERT INTO users(id,email,username,role,created_at) VALUES(2,?,'自检站长','admin',?)", []any{founderEmail, stamp(s.now())}},
			{"INSERT INTO sessions(token_hash,user_id,created_at,expires_at) VALUES(?,1,?,?),(?,2,?,?)", []any{digest(memberCookie), stamp(s.now()), stamp(s.now().Add(time.Hour)), digest(ownerCookie), stamp(s.now()), stamp(s.now().Add(time.Hour))}},
		} {
			if _, err = test.db.ExecContext(ctx, q.sql, q.args...); err != nil {
				fail(w, 500, "初始化自检环境失败")
				return
			}
		}
		do := func(method, path, cookie string, input any) *httptest.ResponseRecorder {
			raw, _ := json.Marshal(input)
			req := httptest.NewRequest(method, "http://diagnostic.invalid"+path, bytes.NewReader(raw)).WithContext(ctx)
			req.Header.Set("Origin", "http://diagnostic.invalid")
			req.Header.Set("Content-Type", "application/json")
			if cookie != "" {
				req.AddCookie(&http.Cookie{Name: "infogap_session", Value: cookie})
			}
			out := httptest.NewRecorder()
			test.routes().ServeHTTP(out, req)
			return out
		}
		if in.Group == "all" || in.Group == "auth" {
			add("未登录与普通用户后台拦截", do("GET", "/api/admin/overview", "", nil).Code == 401 && do("GET", "/api/admin/overview", memberCookie, nil).Code == 403, "匿名请求需登录，普通用户不能进入管理后台")
			add("邀请制注册入口", do("POST", "/api/auth/code", "", map[string]any{"email": "new@example.invalid"}).Code == 403, "没有邀请码的新邮箱不能获取注册验证码")
			_, err = test.db.Exec("INSERT INTO invites(code,max_uses,expires_at,created_at) VALUES('DIAGNOSTIC',1,?,?)", stamp(s.now().Add(time.Hour)), stamp(s.now()))
			out := do("POST", "/api/auth/code", "", map[string]any{"email": "new@example.invalid", "invite": "DIAGNOSTIC"})
			verify := do("POST", "/api/auth/verify", "", map[string]any{"email": "new@example.invalid", "invite": "DIAGNOSTIC", "username": "模拟注册成员", "code": code})
			add("注册自动登录", err == nil && out.Code == 200 && verify.Code == 200 && len(verify.Result().Cookies()) == 1, "模拟邮件验证码完成注册，并检查登录 Cookie；未发送真实邮件")
		}
		if in.Group == "all" || in.Group == "reading" {
			old := stamp(s.now().Add(-96 * time.Hour))
			fresh := stamp(s.now())
			_, err = test.db.Exec(`INSERT INTO gaps(id,title,summary,body,category,level,evidence,source_name,source_url,published_at,released_at,created_at,updated_at,status) VALUES(1,'自检已公开信息','公开摘要','DIAGNOSTIC_PRIVATE_BODY','物理','ordinary','official','自检来源','https://example.invalid/old',?,?,?,?, 'published'),(2,'自检刚发布信息','公开摘要','DIAGNOSTIC_FRESH_BODY','物理','ordinary','official','自检来源','https://example.invalid/fresh',?,?,?,?, 'published')`, old, old, old, old, fresh, fresh, fresh, fresh)
			before := do("GET", "/api/gaps/1", memberCookie, nil)
			add("免费摘要与正文锁定", err == nil && before.Code == 200 && !strings.Contains(before.Body.String(), "DIAGNOSTIC_PRIVATE_BODY") && do("GET", "/api/gaps/2", memberCookie, nil).Code == 200, "普通用户实时查看摘要；未解锁不返回正文")
			unlock := do("POST", "/api/gaps/1/unlock", memberCookie, nil)
			again := do("POST", "/api/gaps/1/unlock", memberCookie, nil)
			var count, used int
			test.db.QueryRow("SELECT COUNT(*) FROM unlocks WHERE user_id=1").Scan(&count)
			test.db.QueryRow("SELECT daily_used FROM users WHERE id=1").Scan(&used)
			add("额度扣减与重复阅读", unlock.Code == 200 && again.Code == 200 && count == 1 && used == 1 && strings.Contains(do("GET", "/api/gaps/1", memberCookie, nil).Body.String(), "DIAGNOSTIC_PRIVATE_BODY"), "首次解锁使用 1 点今日额度；重复阅读不再扣减")
			add("管理员即时全文", strings.Contains(do("GET", "/api/gaps/2", ownerCookie, nil).Body.String(), "DIAGNOSTIC_FRESH_BODY"), "站长可查看刚发布内容的全文")
		}
		if in.Group == "all" || in.Group == "subscriptions" {
			test.db.Exec("UPDATE users SET points=0,membership_until='' WHERE id=1")
			add("普通用户订阅限制", do("POST", "/api/subscriptions", memberCookie, map[string]any{"kind": "category", "value": "物理"}).Code == 403, "普通用户没有有效畅读时不能订阅")
			test.db.Exec("INSERT INTO test_accounts(owner_id,user_id) VALUES(2,1)")
			tx, _ := test.db.Begin()
			err = setTestReading(tx, 1, 1, test.now())
			if err == nil {
				err = tx.Commit()
			} else {
				tx.Rollback()
			}
			ok := err == nil
			for i := 0; i < 40; i++ {
				if do("POST", "/api/subscriptions", memberCookie, map[string]any{"kind": "tag", "value": fmt.Sprintf("自检%d", i)}).Code != 201 {
					ok = false
				}
			}
			add("畅读订阅与数量上限", ok && do("POST", "/api/subscriptions", memberCookie, map[string]any{"kind": "tag", "value": "超出上限"}).Code == 409, "有效畅读可配置40条订阅，第41条被限制")

		}
		if in.Group == "all" || in.Group == "payment" {
			add("仅支付宝及服务器定价", do("POST", "/api/billing/orders", memberCookie, map[string]any{"plan": "credits_100", "channel": "wechat"}).Code == 400 && do("POST", "/api/billing/orders", memberCookie, map[string]any{"plan": "credits_100", "channel": "alipay", "amount_cents": 1}).Code == 400, "拒绝非支付宝渠道与客户端篡改价格")
			created := do("POST", "/api/billing/orders", memberCookie, map[string]any{"plan": "credits_100", "channel": "alipay"})
			var parsed struct{ Order *billingOrder }
			json.Unmarshal(created.Body.Bytes(), &parsed)
			if parsed.Order == nil {
				add("模拟支付宝订单", false, "隔离环境未能生成模拟订单")
			} else {
				id := parsed.Order.ID
				var before, after int
				test.db.QueryRow("SELECT credits FROM users WHERE id=1").Scan(&before)
				status := do("GET", "/api/billing/orders/"+id, memberCookie, nil)
				provider := test.billing.provider.(*diagnosticProvider)
				paid := provider.orders[id]
				err := test.billingApply(paid, "diagnostic-repeat-"+id)
				test.db.QueryRow("SELECT credits FROM users WHERE id=1").Scan(&after)
				add("模拟付款核验与幂等入账", created.Code == 201 && status.Code == 200 && strings.Contains(status.Body.String(), `"status":"success"`) && err == nil && after-before == 1000, "仅隔离数据库模拟到账 1000 积分；重复核验不重复入账")
				paid.AmountCents++
				add("错误金额与订单归属拦截", test.billingApply(paid, "diagnostic-wrong-amount") != nil && do("GET", "/api/billing/orders/"+id, ownerCookie, nil).Code == 404, "错误金额不能结算，其他账号不能查询该订单")
			}
		}
	}
	passed := true
	for _, c := range checks {
		if c.Status == "failed" || c.Status == "unavailable" {
			passed = false
		}
	}
	send(w, 200, map[string]any{"id": "DG-" + token(6), "version": version, "commit": buildCommit, "at": stamp(s.now()), "group": in.Group, "duration_ms": time.Since(started).Milliseconds(), "passed": passed, "checks": checks, "notice": "业务检查在独立内存数据库中执行，不修改正式用户、订单或内容；邮件投递与真实支付到账未在此验证。"})
}
