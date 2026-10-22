package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type campaignReceipt struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
	Permanent bool   `json:"permanent"`
	Campaign  struct {
		EmailCount int `json:"email_count"`
	} `json:"campaign"`
}

func createCampaignFixture(t *testing.T, s *server, admin string, emails []string) campaignReceipt {
	t.Helper()
	w := request(s, "POST", "/api/admin/invites", map[string]any{"max_uses": 100, "days": 30, "emails": emails, "reward_rank": "bronze", "reward_days": 30, "reward_credits": 1000}, admin)
	mustStatus(t, w, 201)
	var receipt campaignReceipt
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || receipt.ID == 0 || receipt.Code == "" {
		t.Fatal("invalid campaign receipt", err, w.Body.String())
	}
	return receipt
}

func campaignCode(t *testing.T, s *server, email, invite string) string {
	t.Helper()
	var code string
	s.mailer = func(_ context.Context, recipient, value string) error {
		if recipient != strings.ToLower(strings.TrimSpace(email)) {
			t.Fatal("email normalization mismatch")
		}
		code = value
		return nil
	}
	w := request(s, "POST", "/api/auth/code", map[string]string{"email": email, "invite": invite}, "")
	mustStatus(t, w, 200)
	if len(code) != 6 || strings.Contains(w.Body.String(), code) {
		t.Fatal("OTP not delivered to isolated fake mailer or exposed")
	}
	return code
}

func campaignRegister(t *testing.T, s *server, email, name, invite string) (*User, string) {
	t.Helper()
	code := campaignCode(t, s, email, invite)
	w := request(s, "POST", "/api/auth/verify", map[string]string{"email": email, "username": name, "invite": invite, "code": code}, "")
	mustStatus(t, w, 200)
	var result struct{ User *User }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.User == nil {
		t.Fatal(err)
	}
	return result.User, w.Result().Cookies()[0].Value
}

func TestInviteCampaignAdminPrivacyValidationAndCodeOnce(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	_, member := account(t, s, "campaign-reader@example.org", "member", 0)
	_, reviewer := account(t, s, "campaign-reviewer@example.org", "reviewer", 0)
	for _, c := range []struct {
		cookie string
		status int
	}{{"", 401}, {member, 403}, {reviewer, 403}} {
		mustStatus(t, request(s, "POST", "/api/admin/invites", map[string]any{"max_uses": 2, "days": 7}, c.cookie), c.status)
	}
	for _, input := range []map[string]any{
		{"emails": []string{"bad-email"}, "reward_rank": "bronze", "reward_days": 30, "reward_credits": 1000},
		{"emails": []string{"test@example.org"}},
		{"emails": []string{}, "reward_rank": "bronze", "reward_days": 30, "reward_credits": 1000},
		{"emails": []string{"test@example.org"}, "reward_rank": "admin", "reward_days": 30, "reward_credits": 1000},
		{"emails": []string{"test@example.org"}, "reward_rank": "bronze", "reward_days": 366, "reward_credits": 1000},
		{"emails": []string{"test@example.org"}, "reward_rank": "bronze", "reward_days": 30, "reward_credits": -1},
	} {
		mustStatus(t, request(s, "POST", "/api/admin/invites", input, admin), 400)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM invites").Scan(&n); err != nil || n != 0 {
		t.Fatal("invalid campaign created an invite", n, err)
	}
	r := createCampaignFixture(t, s, admin, []string{" Selected@Example.org ", "selected@example.org", "second@example.org"})
	if r.Campaign.EmailCount != 2 {
		t.Fatal("email case/space duplicates not removed")
	}
	path := fmt.Sprintf("/api/admin/invites/%d/campaign", r.ID)
	for _, c := range []struct {
		cookie string
		status int
	}{{"", 401}, {member, 403}, {reviewer, 403}} {
		w := request(s, "GET", path, nil, c.cookie)
		mustStatus(t, w, c.status)
		if strings.Contains(w.Body.String(), "selected@example.org") || strings.Contains(w.Body.String(), r.Code) {
			t.Fatal("private invitation leaked")
		}
	}
	w := request(s, "GET", path, nil, admin)
	mustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "selected@example.org") || !strings.Contains(w.Body.String(), `"awarded_count":0`) || strings.Contains(w.Body.String(), r.Code) {
		t.Fatal("admin details missing or repeated invitation code", w.Body.String())
	}
	for _, p := range []string{"/api/bootstrap", "/api/leaderboard", "/api/billing/plans"} {
		cookie := ""
		if p == "/api/leaderboard" {
			cookie = member
		}
		w = request(s, "GET", p, nil, cookie)
		mustStatus(t, w, 200)
		if strings.Contains(w.Body.String(), "selected@example.org") || strings.Contains(w.Body.String(), r.Code) {
			t.Fatal("public API leaked campaign")
		}
	}
	w = request(s, "GET", "/api/admin/overview", nil, admin)
	mustStatus(t, w, 200)
	if strings.Contains(w.Body.String(), r.Code) || strings.Contains(w.Body.String(), "selected@example.org") || !strings.Contains(w.Body.String(), `"campaign":true`) || !strings.Contains(w.Body.String(), `"campaign_email_count":2`) {
		t.Fatal("overview leaked private code/list or lost campaign metadata", w.Body.String())
	}
	var note string
	if err := s.db.QueryRow("SELECT note FROM audit WHERE action='create_invite_campaign'").Scan(&note); err != nil || strings.Contains(note, "@") || strings.Contains(note, r.Code) {
		t.Fatal("audit contains private list/code", err)
	}
}

func TestInviteCampaignRegistrationAwardLoginExpiryAndNoMonthlyGift(t *testing.T) {
	s := testServer(t)
	start := s.now()
	_, admin := account(t, s, founderEmail, "admin", 0)
	existingID, _ := account(t, s, "existing@example.org", "member", 10)
	r := createCampaignFixture(t, s, admin, []string{"selected@example.org", "existing@example.org", "ordinary-invite@example.org"})
	// A correct invitation never substitutes for possession of the email OTP.
	code := campaignCode(t, s, " SELECTED@EXAMPLE.ORG ", r.Code)
	wrongCode := "000000"
	if code == wrongCode {
		wrongCode = "999999"
	}
	mustStatus(t, request(s, "POST", "/api/auth/verify", map[string]string{"email": "selected@example.org", "username": "活动用户", "invite": r.Code, "code": wrongCode}, ""), 400)
	var uses int
	s.db.QueryRow("SELECT uses FROM invites WHERE id=?", r.ID).Scan(&uses)
	if uses != 0 {
		t.Fatal("invalid OTP consumed invitation")
	}
	w := request(s, "POST", "/api/auth/verify", map[string]string{"email": "selected@example.org", "username": "活动用户", "invite": r.Code, "code": code}, "")
	mustStatus(t, w, 200)
	var result struct{ User *User }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	u := result.User
	cookie := w.Result().Cookies()[0].Value
	if u == nil || u.Credits != 1000 || u.Points != 0 || u.TierIndex != 1 || u.EarnedTierIndex != 0 || u.PaidUntil != stamp(start.Add(30*24*time.Hour)) || u.DailyAllowance != 2 || u.RankCredits != 0 || u.Role != "member" || u.Contributions != 0 {
		t.Fatal("incorrect campaign benefits", w.Body.String())
	}
	mustStatus(t, request(s, "POST", "/api/auth/verify", map[string]string{"email": "selected@example.org", "invite": r.Code, "code": code}, ""), 400)
	outsider, _ := campaignRegister(t, s, "outsider@example.org", "名单外用户", r.Code)
	if outsider.Credits != 0 || outsider.TierIndex != 0 || outsider.DailyAllowance != 1 || outsider.PaidUntil != "" {
		t.Fatal("non-member got activity award", outsider)
	}
	// Existing accounts keep their previously granted balance and get no award.
	existing, _ := campaignRegister(t, s, "existing@example.org", "", r.Code)
	if existing.ID != existingID || existing.Credits != 10 || existing.TierIndex != 0 {
		t.Fatal("existing account received registration campaign", existing)
	}
	w = request(s, "POST", "/api/admin/invites", map[string]int{"max_uses": 1, "days": 7}, admin)
	mustStatus(t, w, 201)
	var ordinary campaignReceipt
	json.Unmarshal(w.Body.Bytes(), &ordinary)
	normal, _ := campaignRegister(t, s, "ordinary-invite@example.org", "普通邀请用户", ordinary.Code)
	if normal.Credits != 0 || normal.TierIndex != 0 {
		t.Fatal("allowlist reward escaped its invitation")
	}
	s.now = func() time.Time { return start.Add(2 * time.Minute) }
	logged, _ := campaignRegister(t, s, "selected@example.org", "", r.Code)
	if logged.Credits != 1000 || logged.PaidUntil != u.PaidUntil {
		t.Fatal("login repeated or extended award")
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = grantInviteCampaign(tx, r.ID, u.ID, u.Email, s.now()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"billing_orders", "rank_periods", "rank_gift_events", "rank_credit_batches"} {
		var n int
		if err = s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatal("campaign generated recurring gifts or payment", table, n, err)
		}
	}
	var count, ledgerCount, welcomeCount int
	s.db.QueryRow("SELECT COUNT(*) FROM invite_campaign_awards").Scan(&count)
	s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE kind='invite_campaign'").Scan(&ledgerCount)
	s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE kind='welcome'").Scan(&welcomeCount)
	s.db.QueryRow("SELECT uses FROM invites WHERE id=?", r.ID).Scan(&uses)
	if count != 1 || ledgerCount != 1 || welcomeCount != 0 || uses != 2 {
		t.Fatal("campaign not once-only", count, ledgerCount, welcomeCount, uses)
	}
	if _, err = s.db.Exec("UPDATE sessions SET expires_at=? WHERE user_id=?", stamp(start.Add(60*24*time.Hour)), u.ID); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{start.Add(30*24*time.Hour - time.Second), start.Add(30 * 24 * time.Hour), start.Add(60*24*time.Hour - time.Second)} {
		s.now = func() time.Time { return at }
		w = request(s, "GET", "/api/bootstrap", nil, cookie)
		mustStatus(t, w, 200)
		if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		wantTier := 0
		if at.Before(start.Add(30 * 24 * time.Hour)) {
			wantTier = 1
		}
		if result.User == nil || result.User.TierIndex != wantTier || result.User.Credits != 1000 || result.User.RankCredits != 0 {
			t.Fatal("expiry revived gifts or lost permanent award", w.Body.String())
		}
	}
}

func TestInviteCampaignRegistrationRollbackAndInvitationLimits(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	r := createCampaignFixture(t, s, admin, []string{"rollback@example.org"})
	code := campaignCode(t, s, "rollback@example.org", r.Code)
	if _, err := s.db.Exec("CREATE TRIGGER reject_campaign BEFORE INSERT ON invite_campaign_awards BEGIN SELECT RAISE(ABORT,'isolated failure'); END"); err != nil {
		t.Fatal(err)
	}
	input := map[string]string{"email": "rollback@example.org", "username": "事务测试", "invite": r.Code, "code": code}
	mustStatus(t, request(s, "POST", "/api/auth/verify", input, ""), 500)
	for _, query := range []string{"SELECT uses FROM invites WHERE id=" + fmt.Sprint(r.ID), "SELECT COUNT(*) FROM users WHERE email='rollback@example.org'", "SELECT COUNT(*) FROM ledger WHERE kind='invite_campaign'", "SELECT COUNT(*) FROM invite_campaign_awards"} {
		var n int
		if err := s.db.QueryRow(query).Scan(&n); err != nil || n != 0 {
			t.Fatal("failed award did not roll back", query, n, err)
		}
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_campaign"); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "POST", "/api/auth/verify", input, ""), 200)
	for _, mode := range []string{"revoked", "expired", "exhausted"} {
		email := mode + "@example.org"
		x := createCampaignFixture(t, s, admin, []string{email})
		otp := campaignCode(t, s, email, x.Code)
		switch mode {
		case "revoked":
			mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/invites/%d/revoke", x.ID), nil, admin), 200)
		case "expired":
			s.db.Exec("UPDATE invites SET expires_at=? WHERE id=?", stamp(s.now()), x.ID)
		case "exhausted":
			s.db.Exec("UPDATE invites SET uses=max_uses WHERE id=?", x.ID)
		}
		mustStatus(t, request(s, "POST", "/api/auth/verify", map[string]string{"email": email, "username": "失效" + mode, "invite": x.Code, "code": otp}, ""), 403)
	}
}

func TestInviteCampaignConcurrentVerificationAwardsOnce(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	r := createCampaignFixture(t, s, admin, []string{"concurrent@example.org"})
	code := campaignCode(t, s, "concurrent@example.org", r.Code)
	input := map[string]string{"email": "concurrent@example.org", "username": "并发注册", "invite": r.Code, "code": code}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); statuses <- request(s, "POST", "/api/auth/verify", input, "").Code }()
	}
	wg.Wait()
	close(statuses)
	success, invalid := 0, 0
	for status := range statuses {
		if status == 200 {
			success++
		}
		if status == 400 {
			invalid++
		}
	}
	var awards, uses, credits int
	s.db.QueryRow("SELECT COUNT(*) FROM invite_campaign_awards").Scan(&awards)
	s.db.QueryRow("SELECT uses FROM invites WHERE id=?", r.ID).Scan(&uses)
	s.db.QueryRow("SELECT credits FROM users WHERE email='concurrent@example.org'").Scan(&credits)
	if success != 1 || invalid != 1 || awards != 1 || uses != 1 || credits != 1000 {
		t.Fatal(success, invalid, awards, uses, credits)
	}
}

func TestInviteCampaignThousandAddressesAndBoundedStrictJSON(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	emails := make([]string, 1000)
	for i := range emails {
		emails[i] = fmt.Sprintf("member%04d@%s.example.org", i, strings.Repeat("d", 60))
	}
	r := createCampaignFixture(t, s, admin, emails)
	if r.Campaign.EmailCount != 1000 {
		t.Fatal("valid 1000-address campaign was truncated")
	}
	for _, raw := range []string{
		`{"max_uses":1,"unknown":true}`,
		`{"max_uses":1} {"max_uses":2}`,
		strings.Repeat(" ", inviteCreateBodyLimit) + `{}`,
	} {
		req := httptest.NewRequest("POST", "http://example.com/api/admin/invites", strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("Cookie", "infogap_session="+admin)
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, req)
		mustStatus(t, w, 400)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM invites").Scan(&n); err != nil || n != 1 {
		t.Fatal("invalid large/extra JSON created an invite", n, err)
	}
}

func TestPermanentInviteBeyondHundredUsesAndNinetyDaysUntilRevoked(t *testing.T) {
	s := testServer(t)
	start := s.now()
	_, admin := account(t, s, founderEmail, "admin", 0)
	input := map[string]any{"permanent": true, "max_uses": 1, "days": 1, "emails": []string{"fixed100@example.org"}, "reward_rank": "bronze", "reward_days": 30, "reward_credits": 1000}
	w := request(s, "POST", "/api/admin/invites", input, admin)
	mustStatus(t, w, 201)
	var receipt campaignReceipt
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Permanent || receipt.ExpiresAt != "" || receipt.Code == "" {
		t.Fatal("permanent receipt is not explicit", w.Body.String())
	}
	var permanent, maxUses int
	var expiry string
	if err := s.db.QueryRow("SELECT permanent,max_uses,expires_at FROM invites WHERE id=?", receipt.ID).Scan(&permanent, &maxUses, &expiry); err != nil || permanent != 1 || maxUses != 0 || expiry != "" {
		t.Fatal("permanent invitation still has a hidden cap", permanent, maxUses, expiry, err)
	}
	if _, err := s.db.Exec("UPDATE sessions SET expires_at=?", stamp(start.Add(400*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	var rewarded *User
	for i := 0; i < 101; i++ {
		at := start.Add(120*24*time.Hour + time.Duration(i)*time.Hour)
		s.now = func() time.Time { return at }
		u, _ := campaignRegister(t, s, fmt.Sprintf("fixed%03d@example.org", i), fmt.Sprintf("长期注册%03d", i), receipt.Code)
		if i < 100 {
			if u.TierIndex != 0 || u.Credits != 0 || u.PaidUntil != "" {
				t.Fatal("unlisted permanent-code user got benefits", u)
			}
		} else {
			rewarded = u
			if u.TierIndex != 1 || u.Credits != 1000 || u.PaidUntil != stamp(at.Add(30*24*time.Hour)) {
				t.Fatal("101st registration lost campaign award", u)
			}
		}
	}
	last := s.now()
	s.now = func() time.Time { return last.Add(2 * time.Minute) }
	u, _ := campaignRegister(t, s, "fixed100@example.org", "", receipt.Code)
	if u.Credits != 1000 || u.PaidUntil != rewarded.PaidUntil {
		t.Fatal("permanent invitation repeated or extended reward")
	}
	var uses, awards int
	s.db.QueryRow("SELECT uses FROM invites WHERE id=?", receipt.ID).Scan(&uses)
	s.db.QueryRow("SELECT COUNT(*) FROM invite_campaign_awards WHERE invite_id=?", receipt.ID).Scan(&awards)
	if uses != 101 || awards != 1 {
		t.Fatal("permanent invitation count or once-only award wrong", uses, awards)
	}
	for _, path := range []string{"/api/admin/overview", fmt.Sprintf("/api/admin/invites/%d/campaign", receipt.ID)} {
		w = request(s, "GET", path, nil, admin)
		mustStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), `"permanent":true`) || strings.Contains(w.Body.String(), receipt.Code) {
			t.Fatal("permanent status missing or code repeated", w.Body.String())
		}
	}
	code := campaignCode(t, s, "revoked-fixed@example.org", receipt.Code)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/invites/%d/revoke", receipt.ID), nil, admin), 200)
	mustStatus(t, request(s, "POST", "/api/auth/code", map[string]string{"email": "after-revoke@example.org", "invite": receipt.Code}, ""), 403)
	mustStatus(t, request(s, "POST", "/api/auth/verify", map[string]string{"email": "revoked-fixed@example.org", "username": "撤销后注册", "invite": receipt.Code, "code": code}, ""), 403)
	var remaining int
	s.db.QueryRow("SELECT uses FROM invites WHERE id=?", receipt.ID).Scan(&remaining)
	if remaining != 101 {
		t.Fatal("revoked permanent invitation consumed a use")
	}
}

func appendCampaignEmails(t *testing.T, s *server, admin string, id int64, emails []string) *httptest.ResponseRecorder {
	t.Helper()
	return request(s, "POST", fmt.Sprintf("/api/admin/invites/%d/campaign/emails", id), map[string]any{"emails": emails}, admin)
}

func assertCampaignAppendCounts(t *testing.T, w *httptest.ResponseRecorder, added, existing, total int) {
	t.Helper()
	mustStatus(t, w, 200)
	var got map[string]int
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got) != 3 || got["added"] != added || got["existing"] != existing || got["total"] != total {
		t.Fatal("unexpected append receipt", err, w.Body.String())
	}
}

func TestInviteCampaignAppendPrivacyPermissionsAndRegistrationOnly(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	existingID, member := account(t, s, "append-existing@example.org", "member", 12)
	_, reviewer := account(t, s, "append-reviewer@example.org", "reviewer", 0)
	r := createCampaignFixture(t, s, admin, []string{"append-awarded@example.org"})
	awarded, _ := campaignRegister(t, s, "append-awarded@example.org", "原活动成员", r.Code)
	for _, auth := range []struct {
		cookie string
		status int
	}{{"", 401}, {member, 403}, {reviewer, 403}} {
		w := appendCampaignEmails(t, s, auth.cookie, r.ID, []string{"append-new@example.org"})
		mustStatus(t, w, auth.status)
		if strings.Contains(w.Body.String(), "@") {
			t.Fatal("list leaked in denied response")
		}
	}
	mustStatus(t, request(s, "POST", "/api/admin/test-mode", map[string]any{"tier": 0}, admin), 200)
	mustStatus(t, appendCampaignEmails(t, s, admin, r.ID, []string{"append-new@example.org"}), 403)
	mustStatus(t, request(s, "DELETE", "/api/admin/test-mode", nil, admin), 200)
	batch := []string{" Append-New@Example.org ", "append-new@example.org", "append-awarded@example.org", "append-existing@example.org"}
	assertCampaignAppendCounts(t, appendCampaignEmails(t, s, admin, r.ID, batch), 2, 1, 3)
	assertCampaignAppendCounts(t, appendCampaignEmails(t, s, admin, r.ID, batch), 0, 3, 3)
	var awardCount, ledgerCount, balance, tier, uses, days, credits int
	var rank, until, note string
	if err := s.db.QueryRow("SELECT COUNT(*) FROM invite_campaign_awards").Scan(&awardCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE kind='invite_campaign'").Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT credits,paid_tier,paid_until FROM users WHERE id=?", existingID).Scan(&balance, &tier, &until); err != nil {
		t.Fatal(err)
	}
	if awardCount != 1 || ledgerCount != 1 || balance != 12 || tier != 0 || until != "" {
		t.Fatal("append issued a retroactive award", awardCount, ledgerCount, balance, tier, until)
	}
	if err := s.db.QueryRow("SELECT credits,paid_tier,paid_until FROM users WHERE id=?", awarded.ID).Scan(&balance, &tier, &until); err != nil {
		t.Fatal(err)
	}
	if balance != awarded.Credits || tier != awarded.PaidTier || until != awarded.PaidUntil {
		t.Fatal("append changed prior award")
	}
	if err := s.db.QueryRow("SELECT i.uses,c.reward_rank,c.reward_days,c.reward_credits FROM invites i JOIN invite_campaigns c ON c.invite_id=i.id WHERE i.id=?", r.ID).Scan(&uses, &rank, &days, &credits); err != nil {
		t.Fatal(err)
	}
	if uses != 1 || rank != "bronze" || days != 30 || credits != 1000 {
		t.Fatal("append changed campaign terms", uses, rank, days, credits)
	}
	if err := s.db.QueryRow("SELECT note FROM audit WHERE action='append_invite_campaign_emails' ORDER BY id LIMIT 1").Scan(&note); err != nil || strings.Contains(note, "@") || strings.Contains(note, r.Code) {
		t.Fatal("audit includes private list/code", err, note)
	}
	// Added existing users still get no reward on login; new users receive it only after OTP registration.
	logged, _ := campaignRegister(t, s, "append-existing@example.org", "", r.Code)
	if logged.ID != existingID || logged.Credits != 12 || logged.TierIndex != 0 {
		t.Fatal("existing user received appended reward")
	}
	newUser, _ := campaignRegister(t, s, "append-new@example.org", "新活动成员", r.Code)
	if newUser.Credits != 1000 || newUser.PaidTier != 1 {
		t.Fatal("new appended member did not receive registration-only award")
	}
}

func TestInviteCampaignAppendRequiresUsableCampaign(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	r := createCampaignFixture(t, s, admin, []string{"append-base@example.org"})
	validUntil := stamp(s.now().Add(time.Hour))
	for _, state := range []struct {
		active, permanent, uses, maxUses int
		expires                          string
	}{
		{0, 1, 0, 0, ""}, {1, 0, 0, 100, stamp(s.now().Add(-time.Second))}, {1, 0, 1, 1, validUntil},
	} {
		if _, err := s.db.Exec("UPDATE invites SET active=?,permanent=?,uses=?,max_uses=?,expires_at=? WHERE id=?", state.active, state.permanent, state.uses, state.maxUses, state.expires, r.ID); err != nil {
			t.Fatal(err)
		}
		mustStatus(t, appendCampaignEmails(t, s, admin, r.ID, []string{"append-new@example.org"}), 409)
	}
	// Fixed invitations bypass only expiry/use caps, never revocation.
	if _, err := s.db.Exec("UPDATE invites SET active=1,permanent=1,uses=500,max_uses=1,expires_at=? WHERE id=?", stamp(s.now().Add(-200*24*time.Hour)), r.ID); err != nil {
		t.Fatal(err)
	}
	assertCampaignAppendCounts(t, appendCampaignEmails(t, s, admin, r.ID, []string{"append-new@example.org"}), 1, 0, 2)
	w := request(s, "POST", "/api/admin/invites", map[string]any{"permanent": true}, admin)
	mustStatus(t, w, 201)
	var ordinary campaignReceipt
	if err := json.Unmarshal(w.Body.Bytes(), &ordinary); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, appendCampaignEmails(t, s, admin, ordinary.ID, []string{"append-new@example.org"}), 404)
	mustStatus(t, appendCampaignEmails(t, s, admin, 99999, []string{"append-new@example.org"}), 404)
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM invite_campaign_members WHERE invite_id=?", r.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal(err, count)
	}
}

func TestInviteCampaignAppendValidationThousandLimitAndStrictJSON(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	emails := make([]string, 1000)
	for i := range emails {
		emails[i] = fmt.Sprintf("append%04d@%s.example.org", i, strings.Repeat("d", 60))
	}
	r := createCampaignFixture(t, s, admin, emails[:1])
	for _, input := range [][]string{nil, {}, {"invalid-email"}, make([]string, 1001)} {
		mustStatus(t, appendCampaignEmails(t, s, admin, r.ID, input), 400)
	}
	assertCampaignAppendCounts(t, appendCampaignEmails(t, s, admin, r.ID, emails), 999, 1, 1000)
	mustStatus(t, appendCampaignEmails(t, s, admin, r.ID, []string{emails[0], "over-limit@example.org"}), 409)
	assertCampaignAppendCounts(t, appendCampaignEmails(t, s, admin, r.ID, emails[:1]), 0, 1, 1000)
	for _, raw := range []string{
		`{"emails":["extra@example.org"],"reward_credits":1}`,
		`{"emails":["extra@example.org"]} {"emails":[]}`,
		strings.Repeat(" ", inviteCreateBodyLimit) + `{}`,
	} {
		req := httptest.NewRequest("POST", fmt.Sprintf("http://example.com/api/admin/invites/%d/campaign/emails", r.ID), strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("Cookie", "infogap_session="+admin)
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, req)
		mustStatus(t, w, 400)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM invite_campaign_members WHERE invite_id=?", r.ID).Scan(&count); err != nil || count != 1000 {
		t.Fatal("invalid request changed membership", count, err)
	}
}

func TestInviteCampaignAppendAtomicRollbackAndConcurrentDuplicates(t *testing.T) {
	for _, table := range []string{"invite_campaign_members", "audit"} {
		t.Run(table, func(t *testing.T) {
			s := testServer(t)
			_, admin := account(t, s, founderEmail, "admin", 0)
			r := createCampaignFixture(t, s, admin, []string{"old-member@example.org"})
			when := ""
			if table == "invite_campaign_members" {
				when = " WHEN NEW.email='new-b@example.org'"
			}
			if _, err := s.db.Exec("CREATE TRIGGER reject_append BEFORE INSERT ON " + table + when + " BEGIN SELECT RAISE(ABORT,'test append failure'); END"); err != nil {
				t.Fatal(err)
			}
			batch := []string{"new-a@example.org", "new-b@example.org"}
			mustStatus(t, appendCampaignEmails(t, s, admin, r.ID, batch), 500)
			var members, audits int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM invite_campaign_members WHERE invite_id=?", r.ID).Scan(&members); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow("SELECT COUNT(*) FROM audit WHERE action='append_invite_campaign_emails'").Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if members != 1 || audits != 0 {
				t.Fatal("partial append survived failure", members, audits)
			}
			if _, err := s.db.Exec("DROP TRIGGER reject_append"); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			responses := make(chan *httptest.ResponseRecorder, 6)
			for range 6 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					responses <- request(s, "POST", fmt.Sprintf("/api/admin/invites/%d/campaign/emails", r.ID), map[string]any{"emails": batch}, admin)
				}()
			}
			wg.Wait()
			close(responses)
			added := 0
			for w := range responses {
				mustStatus(t, w, 200)
				var counts map[string]int
				if err := json.Unmarshal(w.Body.Bytes(), &counts); err != nil {
					t.Fatal(err)
				}
				added += counts["added"]
				if counts["total"] != 3 || counts["added"]+counts["existing"] != 2 {
					t.Fatal(counts)
				}
			}
			if added != 2 {
				t.Fatal("concurrent batch duplicated members", added)
			}
		})
	}
}

func TestPermanentInviteMigrationPreservesOrdinaryRules(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	expires := stamp(s.now().Add(7 * 24 * time.Hour))
	if _, err := s.db.Exec("INSERT INTO invites(code,max_uses,uses,expires_at,created_at) VALUES('LEGACY-CODE',2,1,?,?)", expires, stamp(s.now())); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("ALTER TABLE invites DROP COLUMN permanent"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateSchema(s.db); err != nil {
			t.Fatal(err)
		}
	}
	var flag, uses, maxUses int
	var restored string
	if err := s.db.QueryRow("SELECT permanent,uses,max_uses,expires_at FROM invites WHERE code='LEGACY-CODE'").Scan(&flag, &uses, &maxUses, &restored); err != nil || flag != 0 || uses != 1 || maxUses != 2 || restored != expires {
		t.Fatal("migration changed existing invitation", flag, uses, maxUses, restored, err)
	}
	for _, input := range []map[string]any{{"max_uses": 101, "days": 7}, {"max_uses": 1, "days": 91}, {"permanent": false, "max_uses": 101, "days": 91}} {
		mustStatus(t, request(s, "POST", "/api/admin/invites", input, admin), 400)
	}
	w := request(s, "POST", "/api/admin/invites", map[string]any{"permanent": true}, admin)
	mustStatus(t, w, 201)
	var receipt campaignReceipt
	json.Unmarshal(w.Body.Bytes(), &receipt)
	if !receipt.Permanent || receipt.ExpiresAt != "" {
		t.Fatal("ordinary permanent invite missing")
	}
	u, _ := campaignRegister(t, s, "permanent-ordinary@example.org", "永久普通邀请", receipt.Code)
	if u.Credits != 0 || u.TierIndex != 0 {
		t.Fatal("plain permanent invite grants rewards")
	}
	w = request(s, "GET", "/api/admin/overview", nil, admin)
	mustStatus(t, w, 200)
	if strings.Contains(w.Body.String(), receipt.Code) {
		t.Fatal("permanent ordinary invite code leaked in overview")
	}
}
