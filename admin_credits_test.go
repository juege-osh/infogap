package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type adminCreditResponse struct {
	Receipt  adminCreditReceipt `json:"receipt"`
	User     User               `json:"user"`
	Replayed bool               `json:"replayed"`
}

func adminDeduct(t *testing.T, s *server, cookie string, target int64, amount int, reason, key string) *httptest.ResponseRecorder {
	t.Helper()
	return request(s, "POST", fmt.Sprintf("/api/admin/users/%d/credits/deduct", target), adminCreditDeductInput{Amount: amount, Reason: reason, IdempotencyKey: key}, cookie)
}

func adminDeductResponse(t *testing.T, w *httptest.ResponseRecorder) adminCreditResponse {
	t.Helper()
	mustStatus(t, w, 200)
	var out adminCreditResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func deductionCounts(t *testing.T, s *server, target int64) (credits, receipts, ledgers, audits int) {
	t.Helper()
	for _, q := range []struct {
		query string
		out   *int
	}{
		{"SELECT credits FROM users WHERE id=?", &credits},
		{"SELECT COUNT(*) FROM admin_credit_deductions WHERE target_user_id=?", &receipts},
		{"SELECT COUNT(*) FROM ledger WHERE kind='admin_deduct' AND user_id=?", &ledgers},
		{"SELECT COUNT(*) FROM audit WHERE action='admin_deduct' AND object_id=CAST(? AS TEXT)", &audits},
	} {
		if err := s.db.QueryRow(q.query, target).Scan(q.out); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestAdminDeductAuthorizationAndProtectedAccounts(t *testing.T) {
	s := testServer(t)
	owner, admin := account(t, s, founderEmail, "admin", 100)
	member, cookie := account(t, s, "deduct-member@example.org", "member", 100)
	reviewer, review := account(t, s, "deduct-review@example.org", "reviewer", 100)
	for _, c := range []struct {
		cookie string
		status int
	}{{"", 401}, {cookie, 403}, {review, 403}} {
		mustStatus(t, adminDeduct(t, s, c.cookie, member, 10, "纠正重复奖励", "auth-denied-key"), c.status)
	}
	mustStatus(t, adminDeduct(t, s, admin, owner, 10, "纠正重复奖励", "founder-denied-key"), 403)
	mustStatus(t, request(s, "POST", "/api/admin/test-mode", map[string]any{"tier": 0, "credits": 100}, admin), 200)
	var testID int64
	if err := s.db.QueryRow("SELECT user_id FROM test_accounts WHERE owner_id=?", owner).Scan(&testID); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, adminDeduct(t, s, admin, member, 10, "纠正重复奖励", "test-mode-denied"), 403)
	mustStatus(t, request(s, "DELETE", "/api/admin/test-mode", nil, admin), 200)
	mustStatus(t, adminDeduct(t, s, admin, testID, 10, "纠正重复奖励", "test-target-denied"), 403)
	if credits, receipts, ledgers, audits := deductionCounts(t, s, member); credits != 100 || receipts != 0 || ledgers != 0 || audits != 0 {
		t.Fatal(credits, receipts, ledgers, audits)
	}
	// Reviewers are valid targets, and disabling a user does not prevent an admin correction.
	if _, err := s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", reviewer); err != nil {
		t.Fatal(err)
	}
	out := adminDeductResponse(t, adminDeduct(t, s, admin, reviewer, 20, "纠正重复奖励", "reviewer-correction"))
	if out.User.Credits != 80 || !out.User.Disabled || out.Receipt.ActorID != owner {
		t.Fatalf("unexpected target: %+v", out)
	}
}

func TestAdminDeductReceiptReplayAndConflictingKey(t *testing.T) {
	s := testServer(t)
	owner, admin := account(t, s, founderEmail, "admin", 0)
	id, _ := account(t, s, "receipt@example.org", "member", 100)
	other, _ := account(t, s, "other-receipt@example.org", "member", 100)
	first := adminDeductResponse(t, adminDeduct(t, s, admin, id, 30, "纠正重复发放", "immutable-receipt"))
	if first.Replayed || first.User.Credits != 70 || first.Receipt.BeforeCredits != 100 || first.Receipt.AfterCredits != 70 || first.Receipt.Amount != 30 || first.Receipt.ActorID != owner || first.Receipt.TargetUserID != id || first.Receipt.LedgerID == 0 {
		t.Fatalf("receipt mismatch: %+v", first)
	}
	adminDeductResponse(t, adminDeduct(t, s, admin, id, 20, "第二次积分纠正", "second-receipt-key"))
	replay := adminDeductResponse(t, adminDeduct(t, s, admin, id, 30, "纠正重复发放", "immutable-receipt"))
	if !replay.Replayed || replay.Receipt != first.Receipt || replay.User.Credits != 50 {
		t.Fatalf("replay must preserve receipt and load current balance: %+v", replay)
	}
	for _, input := range []struct {
		id     int64
		amount int
		reason string
	}{{id, 31, "纠正重复发放"}, {id, 30, "不同扣减原因"}, {other, 30, "纠正重复发放"}, {id, 30, "纠正重复发放 "}} {
		mustStatus(t, adminDeduct(t, s, admin, input.id, input.amount, input.reason, "immutable-receipt"), 409)
	}
	var ledgerAmount int
	var ledgerKind, ledgerNote, auditNote string
	if err := s.db.QueryRow("SELECT amount,kind,note FROM ledger WHERE id=?", first.Receipt.LedgerID).Scan(&ledgerAmount, &ledgerKind, &ledgerNote); err != nil {
		t.Fatal(err)
	}
	if ledgerAmount != -30 || ledgerKind != "admin_deduct" || !strings.Contains(ledgerNote, first.Receipt.Reason) {
		t.Fatal(ledgerAmount, ledgerKind, ledgerNote)
	}
	if err := s.db.QueryRow("SELECT note FROM audit WHERE action='admin_deduct' ORDER BY id LIMIT 1").Scan(&auditNote); err != nil {
		t.Fatal(err)
	}
	var auditReceipt adminCreditReceipt
	if err := json.Unmarshal([]byte(auditNote), &auditReceipt); err != nil || auditReceipt != first.Receipt {
		t.Fatal("audit snapshot mismatch", err, auditNote)
	}
	if credits, receipts, ledgers, audits := deductionCounts(t, s, id); credits != 50 || receipts != 2 || ledgers != 2 || audits != 2 {
		t.Fatal(credits, receipts, ledgers, audits)
	}
	// The key is also bound to the actor even if a stored record came from another operator.
	if _, err := s.db.Exec("UPDATE admin_credit_deductions SET actor_id=? WHERE idempotency_key=?", other, "immutable-receipt"); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, adminDeduct(t, s, admin, id, 30, "纠正重复发放", "immutable-receipt"), 409)
}

func TestAdminDeductValidationAndInsufficientBalance(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	id, _ := account(t, s, "limits@example.org", "member", 10)
	for _, in := range []adminCreditDeductInput{
		{0, "纠正重复奖励", "valid-request-key"}, {-1, "纠正重复奖励", "valid-request-key"},
		{1, "短", "valid-request-key"}, {1, strings.Repeat("字", 501), "valid-request-key"},
		{1, "   ", "valid-request-key"}, {1, "纠正重复奖励", "short"},
		{1, "纠正重复奖励", strings.Repeat("x", 101)}, {1, "纠正重复奖励", " key-with-space"},
		{1, "纠正重复奖励", "control\nkey"},
	} {
		mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/users/%d/credits/deduct", id), in, admin), 400)
	}
	mustStatus(t, adminDeduct(t, s, admin, 0, 1, "纠正重复奖励", "invalid-target-key"), 400)
	mustStatus(t, adminDeduct(t, s, admin, 99999, 1, "纠正重复奖励", "missing-target-key"), 404)
	mustStatus(t, adminDeduct(t, s, admin, id, 11, "纠正重复奖励", "insufficient-key"), 409)
	if credits, receipts, ledgers, audits := deductionCounts(t, s, id); credits != 10 || receipts != 0 || ledgers != 0 || audits != 0 {
		t.Fatal(credits, receipts, ledgers, audits)
	}
	// Rejected requests do not reserve the key; deducting the exact balance is valid.
	out := adminDeductResponse(t, adminDeduct(t, s, admin, id, 10, "纠正重复奖励", "insufficient-key"))
	if out.User.Credits != 0 {
		t.Fatal(out.User.Credits)
	}
}

func TestAdminDeductAtomicRollback(t *testing.T) {
	for _, table := range []string{"ledger", "admin_credit_deductions", "audit"} {
		t.Run(table, func(t *testing.T) {
			s := testServer(t)
			_, admin := account(t, s, founderEmail, "admin", 0)
			id, _ := account(t, s, "rollback@example.org", "member", 50)
			if _, err := s.db.Exec("CREATE TRIGGER reject_deduction BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT,'test persistence failure'); END"); err != nil {
				t.Fatal(err)
			}
			mustStatus(t, adminDeduct(t, s, admin, id, 20, "纠正重复奖励", "rollback-safe-key"), 500)
			if credits, receipts, ledgers, audits := deductionCounts(t, s, id); credits != 50 || receipts != 0 || ledgers != 0 || audits != 0 {
				t.Fatal(credits, receipts, ledgers, audits)
			}
			if _, err := s.db.Exec("DROP TRIGGER reject_deduction"); err != nil {
				t.Fatal(err)
			}
			out := adminDeductResponse(t, adminDeduct(t, s, admin, id, 20, "纠正重复奖励", "rollback-safe-key"))
			if out.Replayed || out.User.Credits != 30 {
				t.Fatal(out)
			}
		})
	}
}

func TestAdminDeductConcurrentRequestsDoNotDoubleSpend(t *testing.T) {
	for _, sharedKey := range []bool{true, false} {
		t.Run(fmt.Sprint(sharedKey), func(t *testing.T) {
			s := testServer(t)
			_, admin := account(t, s, founderEmail, "admin", 0)
			id, _ := account(t, s, "concurrent@example.org", "member", 30)
			var wg sync.WaitGroup
			results := make(chan *httptest.ResponseRecorder, 12)
			for i := range 12 {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					key := "concurrent-same-key"
					if !sharedKey {
						key = fmt.Sprintf("concurrent-key-%d", i)
					}
					results <- request(s, "POST", fmt.Sprintf("/api/admin/users/%d/credits/deduct", id), adminCreditDeductInput{20, "纠正重复奖励", key}, admin)
				}(i)
			}
			wg.Wait()
			close(results)
			successes, newChanges := 0, 0
			for w := range results {
				if sharedKey || w.Code == 200 {
					out := adminDeductResponse(t, w)
					successes++
					if !out.Replayed {
						newChanges++
					}
				} else {
					mustStatus(t, w, 409)
				}
			}
			if newChanges != 1 || (sharedKey && successes != 12) || (!sharedKey && successes != 1) {
				t.Fatal("concurrent outcomes", sharedKey, successes, newChanges)
			}
			if credits, receipts, ledgers, audits := deductionCounts(t, s, id); credits != 10 || receipts != 1 || ledgers != 1 || audits != 1 {
				t.Fatal(credits, receipts, ledgers, audits)
			}
		})
	}
}

func TestAdminDeductPreservesRankDailyAndGiftBatches(t *testing.T) {
	s, _, id, cookie := billingFixture(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	order := legacyRankPurchase(t, s, cookie, "rank_1_1", "alipay")
	if err := s.billingApply(billingSuccess(order), "deduction-fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE users SET points=250,credits=100,day_key=?,daily_used=2,weekly_used=1,contributions=3,featured_contributions=2 WHERE id=?", dayKey(s.now()), id); err != nil {
		t.Fatal(err)
	}
	before, err := userScan(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", id))
	if err != nil {
		t.Fatal(err)
	}
	beforeBatches, err := activeRankBatches(s.db, id, s.now())
	if err != nil || len(beforeBatches) == 0 {
		t.Fatal(err, beforeBatches)
	}
	// Even ample daily/rank credits cannot cover insufficient permanent credits.
	mustStatus(t, adminDeduct(t, s, admin, id, 101, "纠正重复奖励", "preserve-gifts-key"), 409)
	out := adminDeductResponse(t, adminDeduct(t, s, admin, id, 40, "纠正重复奖励", "preserve-gifts-key"))
	after, err := userScan(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", id))
	if err != nil {
		t.Fatal(err)
	}
	afterBatches, err := activeRankBatches(s.db, id, s.now())
	if err != nil {
		t.Fatal(err)
	}
	before.Credits -= 40
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeBatches, afterBatches) || out.User.Credits != 60 || out.User.RankCredits != beforeBatches[0].Remaining {
		t.Fatalf("unrelated entitlements changed: before=%+v after=%+v batches=%+v/%+v", before, after, beforeBatches, afterBatches)
	}
}
