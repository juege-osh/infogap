package main

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

func legacyInviteFixture(t *testing.T) (*server, int64, string) {
	t.Helper()
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	r := createCampaignFixture(t, s, admin, []string{"legacy-credit@example.org"})
	id, cookie := account(t, s, "legacy-credit@example.org", "member", 0)
	at := stamp(s.now().Add(-60 * 24 * time.Hour))
	result, err := s.db.Exec("INSERT INTO ledger(user_id,kind,amount,created_at,note) VALUES(?,'invite_campaign',1000,?,'legacy permanent invitation fixture')", id, at)
	if err != nil {
		t.Fatal(err)
	}
	ledger, _ := result.LastInsertId()
	if _, err = s.db.Exec("INSERT INTO invite_campaign_awards(user_id,invite_id,email,tier,credits,starts_at,ends_at,ledger_id) VALUES(?,?,?,1,1000,?,?,?)", id, r.ID, "legacy-credit@example.org", at, stamp(s.now().Add(-30*24*time.Hour)), ledger); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE users SET credits=1000 WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	return s, id, cookie
}

func legacyInviteEntry(t *testing.T, s *server, user int64, gap any, kind string, amount int, permanent bool) {
	t.Helper()
	if _, err := s.db.Exec("INSERT INTO ledger(user_id,gap_id,kind,amount,created_at,note) VALUES(?,?,?,?,?,'legacy fixture')", user, gap, kind, amount, stamp(s.now())); err != nil {
		t.Fatal(err)
	}
	if permanent {
		if _, err := s.db.Exec("UPDATE users SET credits=credits+? WHERE id=?", amount, user); err != nil {
			t.Fatal(err)
		}
	}
}

func legacyInviteUnlockFixture(t *testing.T, s *server, user int64, cost int) int64 {
	t.Helper()
	id := gap(t, s, "ordinary", "published", nil)
	if _, err := s.db.Exec("INSERT INTO unlocks(user_id,gap_id,cost,kind,created_at) VALUES(?,?,?,'stored',?)", user, id, cost, stamp(s.now())); err != nil {
		t.Fatal(err)
	}
	legacyInviteEntry(t, s, user, id, "unlock", -cost, true)
	return id
}

func inviteBalance(t *testing.T, s *server, user int64) (int, string) {
	t.Helper()
	n, expires, err := loadInviteCredits(s.db, user, s.now())
	if err != nil {
		t.Fatal(err)
	}
	return n, expires
}

func inviteTx(t *testing.T, s *server, f func(*sql.Tx) error) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = f(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestInviteCreditLegacyMigrationKeepsPurchasedContributionAndOtherPools(t *testing.T) {
	s, user, _ := legacyInviteFixture(t)
	legacyInviteEntry(t, s, user, nil, "purchase", 500, true)
	legacyInviteEntry(t, s, user, nil, "contribution", 200, true)
	id := legacyInviteUnlockFixture(t, s, user, 300)
	legacyInviteEntry(t, s, user, nil, "admin_deduct", -100, true)
	legacyInviteEntry(t, s, user, nil, "reward_revoke", -50, true)
	legacyInviteEntry(t, s, user, nil, "rank_gift", 500, false)
	legacyInviteEntry(t, s, user, nil, "rank_unlock", -20, false)
	legacyInviteEntry(t, s, user, nil, "daily_unlock", -1, false)
	now := s.now()
	if err := initInviteCredits(s.db, now); err != nil {
		t.Fatal(err)
	}
	var permanent, stored, invited, moved, reconciled, spent int
	var reason string
	if err := s.db.QueryRow("SELECT credits FROM users WHERE id=?", user).Scan(&permanent); err != nil {
		t.Fatal(err)
	}
	balance, expires := inviteBalance(t, s, user)
	if permanent != 700 || balance != 550 || permanent+balance != 1250 || expires != stamp(now.Add(30*24*time.Hour)) {
		t.Fatal("wrong migrated ownership", permanent, balance, expires)
	}
	if err := s.db.QueryRow("SELECT cost,invite_cost FROM unlocks WHERE user_id=? AND gap_id=?", user, id).Scan(&stored, &invited); err != nil {
		t.Fatal(err)
	}
	if stored != 0 || invited != 300 {
		t.Fatal("historical unlock not split", stored, invited)
	}
	if err := s.db.QueryRow("SELECT transferred,reconciled,reason,assigned_spend FROM invite_credit_migrations WHERE user_id=?", user).Scan(&moved, &reconciled, &reason, &spent); err != nil {
		t.Fatal(err)
	}
	if moved != 550 || reconciled != 1 || reason != "migrated" || spent != 450 {
		t.Fatal(moved, reconciled, reason, spent)
	}
	if err := initInviteCredits(s.db, now.Add(10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	again, sameExpiry := inviteBalance(t, s, user)
	if again != balance || sameExpiry != expires {
		t.Fatal("migration repeated/extended expiry")
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE user_id=? AND kind IN ('invite_migrate_in','invite_migrate_out')", user).Scan(&count); err != nil || count != 2 {
		t.Fatal(err, count)
	}
}

func TestInviteCreditLegacyMigrationSkipsUnprovableBalances(t *testing.T) {
	for _, mode := range []string{"balance_mismatch", "unknown_ledger_kind", "invalid_award_ledger", "unmatched_unlock"} {
		t.Run(mode, func(t *testing.T) {
			s, user, _ := legacyInviteFixture(t)
			switch mode {
			case "balance_mismatch":
				if _, err := s.db.Exec("UPDATE users SET credits=1050 WHERE id=?", user); err != nil {
					t.Fatal(err)
				}
			case "unknown_ledger_kind":
				legacyInviteEntry(t, s, user, nil, "unclassified_adjustment", 50, true)
			case "invalid_award_ledger":
				if _, err := s.db.Exec("UPDATE ledger SET kind='purchase' WHERE user_id=? AND kind='invite_campaign'", user); err != nil {
					t.Fatal(err)
				}
			case "unmatched_unlock":
				legacyInviteEntry(t, s, user, nil, "unlock", -50, true)
			}
			var before, after, moved, reconciled, count int
			var reason string
			if err := s.db.QueryRow("SELECT credits FROM users WHERE id=?", user).Scan(&before); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := initInviteCredits(s.db, s.now()); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.db.QueryRow("SELECT credits FROM users WHERE id=?", user).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow("SELECT transferred,reconciled,reason FROM invite_credit_migrations WHERE user_id=?", user).Scan(&moved, &reconciled, &reason); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow("SELECT COUNT(*) FROM invite_credit_batches WHERE user_id=?", user).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if before != after || moved != 0 || reconciled != 0 || reason != mode || count != 0 {
				t.Fatal("unproven credit was moved", before, after, moved, reconciled, reason, count)
			}
		})
	}
}

func TestInviteCreditLegacyConfirmedRefundAndUnattributedPositive(t *testing.T) {
	s, user, _ := legacyInviteFixture(t)
	id := legacyInviteUnlockFixture(t, s, user, 200)
	legacyInviteEntry(t, s, user, id, "refund", 200, true)
	if _, err := s.db.Exec("UPDATE unlocks SET refunded=1 WHERE user_id=? AND gap_id=?", user, id); err != nil {
		t.Fatal(err)
	}
	legacyInviteEntry(t, s, user, nil, "refund", 50, true)
	if err := initInviteCredits(s.db, s.now()); err != nil {
		t.Fatal(err)
	}
	var permanent, recovered, spends int
	if err := s.db.QueryRow("SELECT credits FROM users WHERE id=?", user).Scan(&permanent); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT confirmed_refund FROM invite_credit_migrations WHERE user_id=?", user).Scan(&recovered); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM invite_credit_spends WHERE user_id=?", user).Scan(&spends); err != nil {
		t.Fatal(err)
	}
	balance, _ := inviteBalance(t, s, user)
	if permanent != 50 || balance != 1000 || recovered != 200 || spends != 0 {
		t.Fatal(permanent, balance, recovered, spends)
	}
}

func TestInviteCreditLegacyFutureRefundPreservesOriginalBucketAndExpiry(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			s, user, _ := legacyInviteFixture(t)
			legacyInviteEntry(t, s, user, nil, "purchase", 500, true)
			legacyInviteEntry(t, s, user, nil, "admin_deduct", -900, true)
			id := legacyInviteUnlockFixture(t, s, user, 300) // 100 invitation + 200 purchased.
			now := s.now()
			if err := initInviteCredits(s.db, now); err != nil {
				t.Fatal(err)
			}
			if expired {
				s.now = func() time.Time { return now.Add(30 * 24 * time.Hour) }
			}
			for range 2 {
				inviteTx(t, s, func(tx *sql.Tx) error { return refundReadingCredits(tx, id, s.now()) })
			}
			var permanent, cost, inviteCost, refunded int
			if err := s.db.QueryRow("SELECT credits FROM users WHERE id=?", user).Scan(&permanent); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow("SELECT cost,invite_cost,refunded FROM unlocks WHERE user_id=? AND gap_id=?", user, id).Scan(&cost, &inviteCost, &refunded); err != nil {
				t.Fatal(err)
			}
			balance, _ := inviteBalance(t, s, user)
			want := 100
			if expired {
				want = 0
			}
			if permanent != 500 || balance != want || cost != 200 || inviteCost != 100 || refunded != 1 {
				t.Fatal("old refund changed credit type or revived expiry", permanent, balance, cost, inviteCost, refunded)
			}
		})
	}
}

func TestInviteCreditLegacyMigrationRollback(t *testing.T) {
	s, user, _ := legacyInviteFixture(t)
	id := legacyInviteUnlockFixture(t, s, user, 100)
	if _, err := s.db.Exec("CREATE TRIGGER fail_invite_migration BEFORE INSERT ON ledger WHEN NEW.kind='invite_migrate_in' BEGIN SELECT RAISE(ABORT,'test migration failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := initInviteCredits(s.db, s.now()); err == nil {
		t.Fatal("expected atomic migration failure")
	}
	var permanent, batches, receipts, cost, inviteCost int
	if err := s.db.QueryRow("SELECT credits FROM users WHERE id=?", user).Scan(&permanent); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow("SELECT COUNT(*) FROM invite_credit_batches").Scan(&batches)
	s.db.QueryRow("SELECT COUNT(*) FROM invite_credit_migrations").Scan(&receipts)
	s.db.QueryRow("SELECT cost,invite_cost FROM unlocks WHERE user_id=? AND gap_id=?", user, id).Scan(&cost, &inviteCost)
	if permanent != 900 || batches != 0 || receipts != 0 || cost != 100 || inviteCost != 0 {
		t.Fatal("partial migration", permanent, batches, receipts, cost, inviteCost)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_invite_migration"); err != nil {
		t.Fatal(err)
	}
	if err := initInviteCredits(s.db, s.now()); err != nil {
		t.Fatal(err)
	}
	if balance, _ := inviteBalance(t, s, user); balance != 900 {
		t.Fatal(balance)
	}
}

func TestInviteCreditRegistrationSpendRefundExpiryAndRollback(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	r := createCampaignFixture(t, s, admin, []string{"new-invite-credit@example.org"})
	u, _ := campaignRegister(t, s, "new-invite-credit@example.org", "邀请积分用户", r.Code)
	now := s.now()
	if u.Credits != 0 || u.PaidTier != 0 || u.InviteCredits != 1000 || u.InviteCreditsExpiresAt != stamp(now.Add(30*24*time.Hour)) {
		t.Fatal("new invitation changed permanent/tier or missed expiry", u)
	}
	id := gap(t, s, "ordinary", "published", nil)
	inviteTx(t, s, func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO unlocks(user_id,gap_id,cost,invite_cost,kind,created_at) VALUES(?,?,0,150,'invite',?)", u.ID, id, stamp(now)); err != nil {
			return err
		}
		return spendInviteCredits(tx, u.ID, id, 150, now)
	})
	if balance, _ := inviteBalance(t, s, u.ID); balance != 850 {
		t.Fatal(balance)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = spendInviteCredits(tx, u.ID, id, 1, now); err == nil {
		tx.Rollback()
		t.Fatal("duplicate spend accepted")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if balance, _ := inviteBalance(t, s, u.ID); balance != 850 {
		t.Fatal("failed spend not rolled back", balance)
	}
	for range 2 {
		inviteTx(t, s, func(tx *sql.Tx) error { return refundReadingCredits(tx, id, now) })
	}
	if balance, expires := inviteBalance(t, s, u.ID); balance != 1000 || expires != u.InviteCreditsExpiresAt {
		t.Fatal("refund doubled or extended gift", balance, expires)
	}
	second := gap(t, s, "ordinary", "published", nil)
	inviteTx(t, s, func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO unlocks(user_id,gap_id,cost,invite_cost,kind,created_at) VALUES(?,?,0,200,'invite',?)", u.ID, second, stamp(now)); err != nil {
			return err
		}
		return spendInviteCredits(tx, u.ID, second, 200, now)
	})
	s.now = func() time.Time { return now.Add(30 * 24 * time.Hour) }
	if balance, _ := inviteBalance(t, s, u.ID); balance != 0 {
		t.Fatal("expired gift visible before cleanup")
	}
	for range 2 {
		inviteTx(t, s, func(tx *sql.Tx) error {
			if err := expireInviteCredits(tx, u.ID, s.now()); err != nil {
				return err
			}
			return refundReadingCredits(tx, second, s.now())
		})
	}
	var permanent, remaining, expiredCount int
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", u.ID).Scan(&permanent)
	s.db.QueryRow("SELECT remaining FROM invite_credit_batches WHERE user_id=?", u.ID).Scan(&remaining)
	s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE user_id=? AND kind='invite_expire'", u.ID).Scan(&expiredCount)
	if permanent != 0 || remaining != 0 || expiredCount != 1 {
		t.Fatal("expiry revived or duplicate", permanent, remaining, expiredCount)
	}
}

func TestInviteCreditRegistrationBatchFailureIsAtomic(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	r := createCampaignFixture(t, s, admin, []string{"invite-batch-fail@example.org"})
	code := campaignCode(t, s, "invite-batch-fail@example.org", r.Code)
	if _, err := s.db.Exec("CREATE TRIGGER reject_invite_batch BEFORE INSERT ON invite_credit_batches BEGIN SELECT RAISE(ABORT,'test batch failure'); END"); err != nil {
		t.Fatal(err)
	}
	in := map[string]string{"email": "invite-batch-fail@example.org", "username": "批次回滚测试", "invite": r.Code, "code": code}
	mustStatus(t, request(s, "POST", "/api/auth/verify", in, ""), 500)
	for _, query := range []string{"SELECT uses FROM invites WHERE id=" + fmt.Sprint(r.ID), "SELECT COUNT(*) FROM users WHERE email='invite-batch-fail@example.org'", "SELECT COUNT(*) FROM invite_campaign_awards", "SELECT COUNT(*) FROM invite_credit_batches", "SELECT COUNT(*) FROM ledger WHERE kind='invite_campaign'"} {
		var count int
		if err := s.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial registration survived", query, count, err)
		}
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_invite_batch"); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "POST", "/api/auth/verify", in, ""), 200)
}

func TestInviteCreditPublicUnlockUsesGiftWithoutUnlimitedPass(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	r := createCampaignFixture(t, s, admin, []string{"invite-reader@example.org"})
	u, cookie := campaignRegister(t, s, "invite-reader@example.org", "邀请阅读用户", r.Code)
	id := gap(t, s, "featured", "published", nil)
	for range 2 {
		mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", id), nil, cookie), 200)
	}
	var permanent, dailyCost, inviteCost, count int
	if err := s.db.QueryRow("SELECT credits FROM users WHERE id=?", u.ID).Scan(&permanent); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT daily_cost,invite_cost FROM unlocks WHERE user_id=? AND gap_id=?", u.ID, id).Scan(&dailyCost, &inviteCost); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE user_id=? AND kind='invite_unlock'", u.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	balance, _ := inviteBalance(t, s, u.ID)
	if permanent != 0 || dailyCost != 1 || inviteCost != 1 || balance != 999 || count != 1 {
		t.Fatal("invitation granted free unlimited reading or double spent", permanent, dailyCost, inviteCost, balance, count)
	}
}
