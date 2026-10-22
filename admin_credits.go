package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func initAdminCredits(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS admin_credit_deductions(
 idempotency_key TEXT PRIMARY KEY,
 actor_id INTEGER NOT NULL REFERENCES users(id),target_user_id INTEGER NOT NULL REFERENCES users(id),
 amount INTEGER NOT NULL CHECK(amount>0),reason TEXT NOT NULL,
 before_credits INTEGER NOT NULL CHECK(before_credits>=0),after_credits INTEGER NOT NULL CHECK(after_credits>=0),
 ledger_id INTEGER NOT NULL UNIQUE REFERENCES ledger(id),created_at TEXT NOT NULL,
 CHECK(before_credits-after_credits=amount)
);
CREATE INDEX IF NOT EXISTS admin_credit_deductions_target ON admin_credit_deductions(target_user_id,created_at);
`)
	return err
}

type adminCreditDeductInput struct {
	Amount         int    `json:"amount"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

type adminCreditReceipt struct {
	IdempotencyKey string `json:"idempotency_key"`
	ActorID        int64  `json:"actor_id"`
	TargetUserID   int64  `json:"target_user_id"`
	Amount         int    `json:"amount"`
	Reason         string `json:"reason"`
	BeforeCredits  int    `json:"before_credits"`
	AfterCredits   int    `json:"after_credits"`
	LedgerID       int64  `json:"ledger_id"`
	CreatedAt      string `json:"created_at"`
}

const adminCreditReceiptCols = "idempotency_key,actor_id,target_user_id,amount,reason,before_credits,after_credits,ledger_id,created_at"

func scanAdminCreditReceipt(row scanner) (*adminCreditReceipt, error) {
	receipt := &adminCreditReceipt{}
	err := row.Scan(&receipt.IdempotencyKey, &receipt.ActorID, &receipt.TargetUserID, &receipt.Amount, &receipt.Reason, &receipt.BeforeCredits, &receipt.AfterCredits, &receipt.LedgerID, &receipt.CreatedAt)
	return receipt, err
}

func (s *server) deductUserCredits(w http.ResponseWriter, r *http.Request) {
	actor := s.staff(w, r, true)
	if actor == nil {
		return
	}
	if actor.TestMode {
		fail(w, 403, "请先退出测试模式")
		return
	}
	targetID := idParam(r)
	if targetID <= 0 {
		fail(w, 400, "用户编号不正确")
		return
	}
	var in adminCreditDeductInput
	if !decode(w, r, &in) {
		return
	}
	if in.Amount <= 0 {
		fail(w, 400, "扣减积分必须是正整数")
		return
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(in.Reason)); n < 3 || n > 500 || utf8.RuneCountInString(in.Reason) > 500 {
		fail(w, 400, "扣减原因需 3～500 字")
		return
	}
	if n := utf8.RuneCountInString(in.IdempotencyKey); n < 8 || n > 100 || strings.TrimSpace(in.IdempotencyKey) != in.IdempotencyKey || strings.IndexFunc(in.IdempotencyKey, unicode.IsControl) >= 0 {
		fail(w, 400, "请求标识需 8～100 字，不能包含控制字符或首尾空白")
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "扣减积分失败")
		return
	}
	defer tx.Rollback()
	// Recheck the actor inside the same transaction as the balance change.
	cookie, _ := r.Cookie("infogap_session")
	var permitted bool
	err = tx.QueryRowContext(r.Context(), `SELECT role='admin' AND disabled=0 AND NOT EXISTS(SELECT 1 FROM test_sessions WHERE session_hash=?) FROM users WHERE id=?`, digest(cookie.Value), actor.ID).Scan(&permitted)
	if err != nil || !permitted {
		fail(w, 403, "没有操作权限，请确认已退出测试模式")
		return
	}
	receipt, err := scanAdminCreditReceipt(tx.QueryRowContext(r.Context(), "SELECT "+adminCreditReceiptCols+" FROM admin_credit_deductions WHERE idempotency_key=?", in.IdempotencyKey))
	replayed := err == nil
	if err != nil && err != sql.ErrNoRows {
		fail(w, 500, "读取扣减记录失败")
		return
	}
	if replayed && (receipt.ActorID != actor.ID || receipt.TargetUserID != targetID || receipt.Amount != in.Amount || receipt.Reason != in.Reason) {
		fail(w, 409, "此请求标识已用于其他扣减，请勿重复使用")
		return
	}
	target, err := userScan(tx.QueryRowContext(r.Context(), "SELECT "+userCols+" FROM users WHERE id=?", targetID))
	if err == sql.ErrNoRows {
		fail(w, 404, "用户不存在")
		return
	}
	if err != nil {
		fail(w, 500, "读取用户失败")
		return
	}
	if !replayed {
		var testTarget bool
		if err = tx.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM test_accounts WHERE user_id=?)", targetID).Scan(&testTarget); err != nil {
			fail(w, 500, "核对用户失败")
			return
		}
		if target.Role == "admin" || target.Email == founderEmail || testTarget {
			fail(w, 403, "站长及测试账户不能使用正式积分扣减")
			return
		}
		if target.Credits < in.Amount {
			fail(w, 409, "永久积分余额不足，未进行扣减")
			return
		}
		receipt = &adminCreditReceipt{IdempotencyKey: in.IdempotencyKey, ActorID: actor.ID, TargetUserID: targetID, Amount: in.Amount, Reason: in.Reason, BeforeCredits: target.Credits, AfterCredits: target.Credits - in.Amount, CreatedAt: stamp(s.now())}
		res, e := tx.ExecContext(r.Context(), "UPDATE users SET credits=credits-? WHERE id=? AND credits>=?", in.Amount, targetID, in.Amount)
		if e != nil {
			fail(w, 500, "扣减积分失败")
			return
		}
		changed, e := res.RowsAffected()
		if e != nil || changed != 1 {
			fail(w, 409, "永久积分余额已变化，请重新核对")
			return
		}
		res, err = tx.ExecContext(r.Context(), "INSERT INTO ledger(user_id,kind,amount,created_at,note) VALUES(?,'admin_deduct',?,?,?)", targetID, -in.Amount, receipt.CreatedAt, "站长扣减永久积分："+in.Reason)
		if err == nil {
			receipt.LedgerID, err = res.LastInsertId()
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), "INSERT INTO admin_credit_deductions("+adminCreditReceiptCols+") VALUES(?,?,?,?,?,?,?,?,?)", receipt.IdempotencyKey, receipt.ActorID, receipt.TargetUserID, receipt.Amount, receipt.Reason, receipt.BeforeCredits, receipt.AfterCredits, receipt.LedgerID, receipt.CreatedAt)
		}
		if err == nil {
			note, _ := json.Marshal(receipt)
			err = audit(tx, r.Context(), actor.ID, "admin_deduct", strconv.FormatInt(targetID, 10), string(note), receipt.CreatedAt)
		}
		if err != nil {
			fail(w, 500, "扣减记录保存失败，积分未变动")
			return
		}
		target.Credits = receipt.AfterCredits
	}
	// The receipt is immutable; a retry returns the user's current balance.
	s.decorate(target)
	if err = loadRankCredits(tx, target, s.now()); err != nil {
		fail(w, 500, "读取用户权益失败，积分未变动")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "保存扣减失败，请使用同一请求标识重试")
		return
	}
	send(w, 200, map[string]any{"receipt": receipt, "user": target, "replayed": replayed})
}
