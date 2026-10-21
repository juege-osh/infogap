package main

import (
	"context"
	"log"
	"time"
)

// Recover missed callbacks even when the customer closes the payment page.
// Settlement always goes through the same verified provider and atomic writer.
func (s *server) reconcilePayments(ctx context.Context) error {
	if !s.billingEnabled() {
		return nil
	}
	now := s.now()
	cutoff := stamp(now.Add(-30 * time.Second))
	rows, err := s.db.QueryContext(ctx, "SELECT id,amount_cents FROM billing_orders WHERE status IN ('pending','expired') AND created_at>? AND last_checked_at<=? ORDER BY last_checked_at,id LIMIT 5", stamp(now.Add(-72*time.Hour)), cutoff)
	if err != nil {
		return err
	}
	type pending struct {
		id     string
		amount int64
	}
	orders := []pending{}
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.amount); err != nil {
			rows.Close()
			return err
		}
		orders = append(orders, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, order := range orders {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		claim, err := s.db.ExecContext(ctx, "UPDATE billing_orders SET last_checked_at=? WHERE id=? AND status IN ('pending','expired') AND last_checked_at<=?", stamp(s.now()), order.id, cutoff)
		if err != nil {
			return err
		}
		count, err := claim.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			continue
		}
		result, err := s.billing.provider.QueryPayment(order.id, order.amount)
		if err == nil && result.PaymentNo == order.id {
			err = s.billingApply(result, "query:"+digest(result.ResponsePayload+result.PaymentNo+result.TransactionStatus))
			if err == nil && result.TransactionStatus == "S" {
				log.Printf("payment_reconciled order=%s", order.id)
			}
		} else if err == nil {
			log.Printf("payment_reconcile_failed order=%s reason=order_mismatch", order.id)
			continue
		}
		if err != nil {
			log.Printf("payment_reconcile_failed order=%s error=%v", order.id, err)
		}
	}
	return nil
}

func (s *server) runPaymentReconciliation(ctx context.Context) {
	if !s.billingEnabled() {
		return
	}
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := s.reconcilePayments(ctx); err != nil && ctx.Err() == nil {
			log.Printf("payment_reconcile_scan_failed error=%v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
