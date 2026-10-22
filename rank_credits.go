package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const rankGiftPolicyVersion = 2

const rankCreditSchema = `
CREATE TABLE IF NOT EXISTS rank_credit_batches(
 id INTEGER PRIMARY KEY AUTOINCREMENT,period_id INTEGER NOT NULL REFERENCES rank_periods(id),user_id INTEGER NOT NULL REFERENCES users(id),
 entitlement_total INTEGER NOT NULL,amount INTEGER NOT NULL CHECK(amount>0),remaining INTEGER NOT NULL CHECK(remaining>=0 AND remaining<=amount),
 granted_at TEXT NOT NULL,expires_at TEXT NOT NULL,expired_at TEXT NOT NULL DEFAULT '',policy_version INTEGER NOT NULL DEFAULT 2 CHECK(policy_version=2),
 UNIQUE(period_id,entitlement_total)
);
CREATE INDEX IF NOT EXISTS rank_credit_user_expiry ON rank_credit_batches(user_id,expires_at);
CREATE TABLE IF NOT EXISTS rank_credit_spends(
 user_id INTEGER NOT NULL,gap_id INTEGER NOT NULL,batch_id INTEGER NOT NULL REFERENCES rank_credit_batches(id),amount INTEGER NOT NULL CHECK(amount>0),refunded INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(user_id,gap_id,batch_id),FOREIGN KEY(user_id,gap_id) REFERENCES unlocks(user_id,gap_id) ON DELETE CASCADE
);`

func migrateRankCredits(db *sql.DB) error {
	for _, field := range []struct{ table, name, decl string }{
		{"rank_gift_events", "policy_version", "INTEGER NOT NULL DEFAULT 1"},
		{"rank_gift_events", "expires_at", "TEXT NOT NULL DEFAULT ''"},
		{"ledger", "expires_at", "TEXT NOT NULL DEFAULT ''"},
		{"unlocks", "rank_cost", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := ensureColumn(db, field.table, field.name, field.decl); err != nil {
			return err
		}
	}
	_, err := db.Exec(rankCreditSchema)
	return err
}

type rankCreditReader interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

func loadRankCredits(q rankCreditReader, u *User, now time.Time) error {
	var next sql.NullString
	err := q.QueryRow("SELECT COALESCE(SUM(remaining),0),MIN(expires_at) FROM rank_credit_batches WHERE user_id=? AND remaining>0 AND expires_at>? AND expired_at=''", u.ID, stamp(now)).Scan(&u.RankCredits, &next)
	u.RankCreditsExpiresAt = next.String
	u.AvailableCredits = u.DailyRemaining + u.RankCredits + u.Credits
	return err
}

// Call outside an open transaction/rows. Transactional readers pass their own
// connection to loadRankCredits, avoiding a second SQLite connection/deadlock.
func (s *server) decorateWithRank(u *User) *User {
	s.decorate(u)
	if u != nil && u.ID > 0 {
		if err := loadRankCredits(s.db, u, s.now()); err != nil {
			u.RankCredits, u.RankCreditsExpiresAt = 0, ""
			u.AvailableCredits = u.DailyRemaining + u.Credits
		}
	}
	return u
}

type rankCreditBatch struct {
	ID            int64  `json:"id"`
	Amount        int    `json:"amount"`
	Remaining     int    `json:"remaining"`
	GrantedAt     string `json:"granted_at"`
	ExpiresAt     string `json:"expires_at"`
	PolicyVersion int    `json:"policy_version"`
}

func activeRankBatches(q rankCreditReader, user int64, now time.Time) ([]rankCreditBatch, error) {
	rows, err := q.Query("SELECT id,amount,remaining,granted_at,expires_at,policy_version FROM rank_credit_batches WHERE user_id=? AND remaining>0 AND expires_at>? AND expired_at='' ORDER BY expires_at,id", user, stamp(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []rankCreditBatch{}
	for rows.Next() {
		var b rankCreditBatch
		if err := rows.Scan(&b.ID, &b.Amount, &b.Remaining, &b.GrantedAt, &b.ExpiresAt, &b.PolicyVersion); err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	return items, rows.Err()
}

func expireRankCredits(tx *sql.Tx, user int64, now time.Time) error {
	rows, err := tx.Query("SELECT id,remaining,expires_at FROM rank_credit_batches WHERE user_id=? AND expired_at='' AND expires_at<=?", user, stamp(now))
	if err != nil {
		return err
	}
	items := []rankCreditBatch{}
	for rows.Next() {
		var b rankCreditBatch
		if err = rows.Scan(&b.ID, &b.Remaining, &b.ExpiresAt); err != nil {
			rows.Close()
			return err
		}
		items = append(items, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, b := range items {
		if b.Remaining > 0 {
			if _, err = tx.Exec("INSERT INTO ledger(user_id,kind,amount,created_at,note,expires_at) VALUES(?,'rank_expire',?,?,?,?)", user, -b.Remaining, stamp(now), fmt.Sprintf("段位赠分到期 · 批次 %d", b.ID), b.ExpiresAt); err != nil {
				return err
			}
		}
		if _, err = tx.Exec("UPDATE rank_credit_batches SET remaining=0,expired_at=? WHERE id=?", stamp(now), b.ID); err != nil {
			return err
		}
	}
	return nil
}

func spendRankCredits(tx *sql.Tx, user, gap int64, amount int, now time.Time) error {
	if amount == 0 {
		return nil
	}
	batches, err := activeRankBatches(tx, user, now)
	if err != nil {
		return err
	}
	remaining := amount
	for _, b := range batches {
		cost := min(remaining, b.Remaining)
		if cost == 0 {
			break
		}
		if _, err = tx.Exec("UPDATE rank_credit_batches SET remaining=remaining-? WHERE id=?", cost, b.ID); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO rank_credit_spends(user_id,gap_id,batch_id,amount) VALUES(?,?,?,?)", user, gap, b.ID, cost); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO ledger(user_id,gap_id,kind,amount,created_at,note,expires_at) VALUES(?,?,'rank_unlock',?,?,?,?)", user, gap, -cost, stamp(now), fmt.Sprintf("使用段位赠分解锁 · 批次 %d", b.ID), b.ExpiresAt); err != nil {
			return err
		}
		remaining -= cost
	}
	if remaining != 0 {
		return errors.New("rank gift balance changed")
	}
	return nil
}

func refundRankCredits(tx *sql.Tx, user, gap int64, now time.Time) error {
	rows, err := tx.Query("SELECT s.batch_id,s.amount,b.expires_at,b.expired_at FROM rank_credit_spends s JOIN rank_credit_batches b ON b.id=s.batch_id WHERE s.user_id=? AND s.gap_id=? AND s.refunded=0", user, gap)
	if err != nil {
		return err
	}
	type refund struct {
		id               int64
		amount           int
		expires, expired string
	}
	items := []refund{}
	for rows.Next() {
		var v refund
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
			if _, err = tx.Exec("UPDATE rank_credit_batches SET remaining=remaining+? WHERE id=?", v.amount, v.id); err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO ledger(user_id,gap_id,kind,amount,created_at,note,expires_at) VALUES(?,?,'rank_refund',?,?,?,?)", user, gap, v.amount, stamp(now), fmt.Sprintf("信息证伪，退回原段位赠分批次 %d；到期不延长", v.id), v.expires); err != nil {
				return err
			}
		}
		if _, err = tx.Exec("UPDATE rank_credit_spends SET refunded=1 WHERE user_id=? AND gap_id=? AND batch_id=?", user, gap, v.id); err != nil {
			return err
		}
	}
	return nil
}
