package main

import (
	"database/sql"
	"fmt"
	"time"
)

var unlockCosts = map[string]int{"ordinary": 1, "featured": 2, "rare": 4, "exclusive": 8}
var readingZone = time.FixedZone("Asia/Shanghai", 8*60*60)

func dayKey(t time.Time) string { return t.In(readingZone).Format("2006-01-02") }

func migrateReadingCredits(db *sql.DB) error {
	for _, migration := range []struct{ table, name, declaration string }{
		{"users", "day_key", "TEXT NOT NULL DEFAULT ''"},
		{"users", "daily_used", "INTEGER NOT NULL DEFAULT 0 CHECK(daily_used>=0)"},
		{"unlocks", "daily_cost", "INTEGER NOT NULL DEFAULT 0"},
		{"unlocks", "day_key", "TEXT NOT NULL DEFAULT ''"},
	} {
		rows, err := db.Query("PRAGMA table_info(" + migration.table + ")")
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var def any
			if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			found = found || name == migration.name
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if !found {
			if _, err = db.Exec("ALTER TABLE " + migration.table + " ADD COLUMN " + migration.name + " " + migration.declaration); err != nil {
				return err
			}
		}
	}
	return nil
}

// Long-term credit refunds never expire. Daily gifts return only to the day
// they were spent, so a correction cannot turn expired gifts into stored money.
func refundReadingCredits(tx *sql.Tx, gapID int64, now time.Time) error {
	type refund struct {
		user          int64
		stored, daily int
		day           string
	}
	rows, err := tx.Query("SELECT user_id,cost,daily_cost,day_key FROM unlocks WHERE gap_id=? AND refunded=0", gapID)
	if err != nil {
		return err
	}
	items := []refund{}
	for rows.Next() {
		var item refund
		if err = rows.Scan(&item.user, &item.stored, &item.daily, &item.day); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.stored > 0 {
			if _, err = tx.Exec("UPDATE users SET credits=credits+? WHERE id=?", item.stored, item.user); err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO ledger(user_id,gap_id,kind,amount,created_at,note) VALUES(?,?,'refund',?,?,'信息证伪，退还长期阅读额度')", item.user, gapID, item.stored, stamp(now)); err != nil {
				return err
			}
		}
		if item.daily > 0 && item.day == dayKey(now) {
			result, err := tx.Exec("UPDATE users SET daily_used=max(0,daily_used-?) WHERE id=? AND day_key=?", item.daily, item.user, item.day)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if changed != 1 {
				return fmt.Errorf("daily refund state mismatch")
			}
			if _, err = tx.Exec("INSERT INTO ledger(user_id,gap_id,kind,amount,created_at,note) VALUES(?,?,'daily_refund',?,?,'信息证伪，退回今日赠送额度；当日有效')", item.user, gapID, item.daily, stamp(now)); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec("UPDATE unlocks SET refunded=1 WHERE gap_id=?", gapID)
	return err
}
