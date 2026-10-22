package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

const inviteCreditDays = 30
const inviteCreditSchema = `
CREATE TABLE IF NOT EXISTS invite_credit_batches(
 id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER NOT NULL UNIQUE REFERENCES invite_campaign_awards(user_id),
 award_ledger_id INTEGER NOT NULL UNIQUE REFERENCES ledger(id),amount INTEGER NOT NULL CHECK(amount>0),
 remaining INTEGER NOT NULL CHECK(remaining>=0 AND remaining<=amount),granted_at TEXT NOT NULL,expires_at TEXT NOT NULL,
 expired_at TEXT NOT NULL DEFAULT '',origin TEXT NOT NULL CHECK(origin IN ('registration','legacy_migration'))
);
CREATE INDEX IF NOT EXISTS invite_credit_user_expiry ON invite_credit_batches(user_id,expires_at);
CREATE TABLE IF NOT EXISTS invite_credit_spends(
 user_id INTEGER NOT NULL,gap_id INTEGER NOT NULL,batch_id INTEGER NOT NULL REFERENCES invite_credit_batches(id),
 amount INTEGER NOT NULL CHECK(amount>0),refunded INTEGER NOT NULL DEFAULT 0 CHECK(refunded IN (0,1)),
 legacy INTEGER NOT NULL DEFAULT 0 CHECK(legacy IN (0,1)),PRIMARY KEY(user_id,gap_id,batch_id),
 FOREIGN KEY(user_id,gap_id) REFERENCES unlocks(user_id,gap_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS invite_credit_migrations(
 user_id INTEGER PRIMARY KEY REFERENCES invite_campaign_awards(user_id),award_ledger_id INTEGER NOT NULL UNIQUE,
 award_credits INTEGER NOT NULL,balance_before INTEGER NOT NULL,expected_balance INTEGER NOT NULL,
 transferred INTEGER NOT NULL CHECK(transferred>=0),reconciled INTEGER NOT NULL CHECK(reconciled IN (0,1)),
 reason TEXT NOT NULL,ledger_cutoff INTEGER NOT NULL,assigned_spend INTEGER NOT NULL,confirmed_refund INTEGER NOT NULL,
 batch_id INTEGER REFERENCES invite_credit_batches(id),created_at TEXT NOT NULL
);`

type inviteCreditBatch struct {
	ID        int64  `json:"id"`
	Amount    int    `json:"amount"`
	Remaining int    `json:"remaining"`
	GrantedAt string `json:"granted_at"`
	ExpiresAt string `json:"expires_at"`
}

// The migration runs once per old award. Every skipped/zero transfer has a
// receipt explaining why; a restart cannot silently recalculate or debit again.
func initInviteCredits(db *sql.DB, now time.Time) error {
	if err := ensureColumn(db, "unlocks", "invite_cost", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if _, err := db.Exec(inviteCreditSchema); err != nil {
		return err
	}
	rows, err := db.Query(`SELECT a.user_id FROM invite_campaign_awards a WHERE NOT EXISTS(SELECT 1 FROM invite_credit_batches b WHERE b.user_id=a.user_id) AND NOT EXISTS(SELECT 1 FROM invite_credit_migrations m WHERE m.user_id=a.user_id) ORDER BY a.user_id`)
	if err != nil {
		return err
	}
	users := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		users = append(users, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range users {
		if err = migrateLegacyInviteCredit(db, id, now); err != nil {
			return err
		}
	}
	return nil
}

type legacyInviteUnlock struct {
	cost, invite        int64
	refunded, confirmed bool
}

func migrateLegacyInviteCredit(db *sql.DB, user int64, now time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM invite_credit_migrations WHERE user_id=?) OR EXISTS(SELECT 1 FROM invite_credit_batches WHERE user_id=?)`, user, user).Scan(&done); err != nil || done {
		return err
	}
	var award, balance, awardLedger int64
	if err = tx.QueryRow("SELECT a.credits,u.credits,a.ledger_id FROM invite_campaign_awards a JOIN users u ON u.id=a.user_id WHERE a.user_id=?", user).Scan(&award, &balance, &awardLedger); err != nil {
		return err
	}
	unlockRows, err := tx.Query("SELECT gap_id,cost,refunded FROM unlocks WHERE user_id=?", user)
	if err != nil {
		return err
	}
	unlocks := map[int64]*legacyInviteUnlock{}
	for unlockRows.Next() {
		var id, cost int64
		var refunded bool
		if err = unlockRows.Scan(&id, &cost, &refunded); err != nil {
			unlockRows.Close()
			return err
		}
		unlocks[id] = &legacyInviteUnlock{cost: cost, refunded: refunded}
	}
	err = unlockRows.Err()
	unlockRows.Close()
	if err != nil {
		return err
	}
	rows, err := tx.Query("SELECT id,gap_id,kind,amount FROM ledger WHERE user_id=? ORDER BY id", user)
	if err != nil {
		return err
	}
	expected, pool, cutoff, spent, recovered := int64(0), int64(0), int64(0), int64(0), int64(0)
	reason := ""
	awardSeen := false
	spentGaps := map[int64]bool{}
	for rows.Next() {
		var id, amount int64
		var gap sql.NullInt64
		var kind string
		if err = rows.Scan(&id, &gap, &kind, &amount); err != nil {
			rows.Close()
			return err
		}
		cutoff = id
		permanent := false
		switch kind {
		case "welcome", "purchase", "purchase_topup", "contribution", "feedback_reward", "rank_monthly_gift", "invite_campaign", "refund":
			permanent = true
			if amount < 0 {
				reason = "invalid_ledger_sign"
			}
		case "unlock", "admin_deduct", "reward_revoke":
			permanent = true
			if amount > 0 || amount == math.MinInt64 {
				reason = "invalid_ledger_sign"
			}
		case "daily_unlock", "daily_refund", "rank_gift", "rank_unlock", "rank_refund", "rank_expire":
			// These amounts never entered users.credits.
		default:
			reason = "unknown_ledger_kind"
		}
		if permanent {
			if (amount > 0 && expected > math.MaxInt64-amount) || (amount < 0 && expected < math.MinInt64-amount) {
				reason = "ledger_overflow"
			} else {
				expected += amount
			}
		}
		if id == awardLedger {
			awardSeen = kind == "invite_campaign" && amount == award && award > 0
			if awardSeen {
				pool = award
			} else {
				reason = "invalid_award_ledger"
			}
			continue
		}
		if id < awardLedger || !awardSeen || !permanent || amount == math.MinInt64 {
			continue
		}
		if amount < 0 {
			assigned := min(pool, -amount)
			pool -= assigned
			spent += assigned
			if kind == "unlock" && assigned > 0 {
				u := unlocks[gap.Int64]
				if !gap.Valid || u == nil || u.cost != -amount || spentGaps[gap.Int64] {
					reason = "unmatched_unlock"
				} else {
					u.invite = assigned
					spentGaps[gap.Int64] = true
				}
			}
		} else if kind == "refund" && gap.Valid {
			u := unlocks[gap.Int64]
			// Only an exact one-time reversal of a proven old unlock restores the
			// invitation pool. Unattributed positive refunds stay permanent.
			if u != nil && u.invite > 0 && u.cost == amount && u.refunded && !u.confirmed {
				pool += u.invite
				recovered += u.invite
				u.confirmed = true
			} else if u != nil && u.invite > 0 && !u.refunded {
				reason = "refund_state_mismatch"
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !awardSeen {
		reason = "invalid_award_ledger"
	}
	if reason == "" && expected != balance {
		reason = "balance_mismatch"
	}
	if reason == "" && (pool < 0 || pool > award) {
		reason = "invalid_invite_pool"
	}
	var isTest bool
	if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM test_accounts WHERE user_id=?)", user).Scan(&isTest); err != nil {
		return err
	}
	if isTest {
		reason = "test_account"
	}
	reconciled := reason == ""
	transferred := int64(0)
	var batchID any
	at, expires := stamp(now), stamp(now.Add(inviteCreditDays*24*time.Hour))
	if reconciled {
		transferred = min(pool, balance)
		reason = "migrated"
		if transferred == 0 {
			reason = "fully_consumed"
		}
		result, e := tx.Exec("INSERT INTO invite_credit_batches(user_id,award_ledger_id,amount,remaining,granted_at,expires_at,origin) VALUES(?,?,?,?,?,?,'legacy_migration')", user, awardLedger, award, transferred, at, expires)
		if e != nil {
			return e
		}
		id, e := result.LastInsertId()
		if e != nil {
			return e
		}
		batchID = id
		if transferred > 0 {
			result, e = tx.Exec("UPDATE users SET credits=credits-? WHERE id=? AND credits>=?", transferred, user, transferred)
			if e != nil {
				return e
			}
			changed, e := result.RowsAffected()
			if e != nil {
				return e
			}
			if changed != 1 {
				return errors.New("legacy invite balance changed")
			}
			if _, e = tx.Exec("INSERT INTO ledger(user_id,kind,amount,created_at,note) VALUES(?,'invite_migrate_out',?,?,'旧邀请赠分余量转入30天批次；不减少总可用积分')", user, -transferred, at); e != nil {
				return e
			}
			if _, e = tx.Exec("INSERT INTO ledger(user_id,kind,amount,created_at,note,expires_at) VALUES(?,'invite_migrate_in',?,?,'旧邀请赠分余量迁移；迁移日起30天有效',?)", user, transferred, at, expires); e != nil {
				return e
			}
		}
		for gap, u := range unlocks {
			if u.invite == 0 || u.refunded {
				continue
			}
			if _, e = tx.Exec("INSERT INTO invite_credit_spends(user_id,gap_id,batch_id,amount,legacy) VALUES(?,?,?,?,1)", user, gap, id, u.invite); e != nil {
				return e
			}
			result, e = tx.Exec("UPDATE unlocks SET cost=cost-?,invite_cost=invite_cost+? WHERE user_id=? AND gap_id=? AND cost=? AND refunded=0", u.invite, u.invite, user, gap, u.cost)
			if e != nil {
				return e
			}
			changed, e := result.RowsAffected()
			if e != nil {
				return e
			}
			if changed != 1 {
				return errors.New("legacy invite unlock changed")
			}
		}
	}
	if _, err = tx.Exec("INSERT INTO invite_credit_migrations(user_id,award_ledger_id,award_credits,balance_before,expected_balance,transferred,reconciled,reason,ledger_cutoff,assigned_spend,confirmed_refund,batch_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", user, awardLedger, award, balance, expected, transferred, reconciled, reason, cutoff, spent, recovered, batchID, at); err != nil {
		return err
	}
	return tx.Commit()
}

func loadInviteCredits(q rankCreditReader, user int64, now time.Time) (int, string, error) {
	var balance int
	var next sql.NullString
	err := q.QueryRow("SELECT COALESCE(SUM(remaining),0),MIN(expires_at) FROM invite_credit_batches WHERE user_id=? AND remaining>0 AND expires_at>? AND expired_at=''", user, stamp(now)).Scan(&balance, &next)
	return balance, next.String, err
}

func activeInviteBatches(q rankCreditReader, user int64, now time.Time) ([]inviteCreditBatch, error) {
	rows, err := q.Query("SELECT id,amount,remaining,granted_at,expires_at FROM invite_credit_batches WHERE user_id=? AND remaining>0 AND expires_at>? AND expired_at='' ORDER BY expires_at,id", user, stamp(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []inviteCreditBatch{}
	for rows.Next() {
		var b inviteCreditBatch
		if err = rows.Scan(&b.ID, &b.Amount, &b.Remaining, &b.GrantedAt, &b.ExpiresAt); err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	return items, rows.Err()
}

func spendInviteCredits(tx *sql.Tx, user, gap int64, amount int, now time.Time) error {
	if amount == 0 {
		return nil
	}
	if amount < 0 {
		return errors.New("invalid invite spend")
	}
	batches, err := activeInviteBatches(tx, user, now)
	if err != nil {
		return err
	}
	left := amount
	for _, b := range batches {
		cost := min(left, b.Remaining)
		if cost == 0 {
			break
		}
		if _, err = tx.Exec("UPDATE invite_credit_batches SET remaining=remaining-? WHERE id=?", cost, b.ID); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO invite_credit_spends(user_id,gap_id,batch_id,amount) VALUES(?,?,?,?)", user, gap, b.ID, cost); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO ledger(user_id,gap_id,kind,amount,created_at,note,expires_at) VALUES(?,?,'invite_unlock',?,?,?,?)", user, gap, -cost, stamp(now), fmt.Sprintf("使用邀请赠分解锁 · 批次 %d", b.ID), b.ExpiresAt); err != nil {
			return err
		}
		left -= cost
	}
	if left != 0 {
		return errors.New("invite gift balance changed")
	}
	return nil
}

func refundInviteCredits(tx *sql.Tx, user, gap int64, now time.Time) error {
	rows, err := tx.Query("SELECT s.batch_id,s.amount,b.expires_at,b.expired_at FROM invite_credit_spends s JOIN invite_credit_batches b ON b.id=s.batch_id WHERE s.user_id=? AND s.gap_id=? AND s.refunded=0", user, gap)
	if err != nil {
		return err
	}
	type item struct {
		id               int64
		amount           int
		expires, expired string
	}
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.amount, &v.expires, &v.expired); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		if v.expires > stamp(now) && v.expired == "" {
			if _, err = tx.Exec("UPDATE invite_credit_batches SET remaining=remaining+? WHERE id=?", v.amount, v.id); err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO ledger(user_id,gap_id,kind,amount,created_at,note,expires_at) VALUES(?,?,'invite_refund',?,?,?,?)", user, gap, v.amount, stamp(now), fmt.Sprintf("信息证伪，退回原邀请赠分批次 %d；到期不延长", v.id), v.expires); err != nil {
				return err
			}
		}
		if _, err = tx.Exec("UPDATE invite_credit_spends SET refunded=1 WHERE user_id=? AND gap_id=? AND batch_id=?", user, gap, v.id); err != nil {
			return err
		}
	}
	return nil
}

func expireInviteCredits(tx *sql.Tx, user int64, now time.Time) error {
	rows, err := tx.Query("SELECT id,remaining,expires_at FROM invite_credit_batches WHERE user_id=? AND expired_at='' AND expires_at<=?", user, stamp(now))
	if err != nil {
		return err
	}
	items := []inviteCreditBatch{}
	for rows.Next() {
		var v inviteCreditBatch
		if err = rows.Scan(&v.ID, &v.Remaining, &v.ExpiresAt); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		if v.Remaining > 0 {
			if _, err = tx.Exec("INSERT INTO ledger(user_id,kind,amount,created_at,note,expires_at) VALUES(?,'invite_expire',?,?,?,?)", user, -v.Remaining, stamp(now), fmt.Sprintf("邀请赠分到期 · 批次 %d", v.ID), v.ExpiresAt); err != nil {
				return err
			}
		}
		if _, err = tx.Exec("UPDATE invite_credit_batches SET remaining=0,expired_at=? WHERE id=?", stamp(now), v.ID); err != nil {
			return err
		}
	}
	return nil
}
