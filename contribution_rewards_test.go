package main

import (
	"fmt"
	"testing"
)

func TestContributionRewardsByReviewedLevel(t *testing.T) {
	for _, tc := range []struct {
		level           string
		points, credits int
	}{{"ordinary", 20, 100}, {"featured", 80, 200}, {"rare", 200, 400}, {"exclusive", 500, 800}} {
		t.Run(tc.level, func(t *testing.T) {
			s := testServer(t)
			_, admin := account(t, s, founderEmail, "admin", 0)
			id, _ := account(t, s, "author@example.test", "member", 10)
			g := gap(t, s, tc.level, "pending", id)
			path := fmt.Sprintf("/api/admin/review/%d", g)
			in := map[string]string{"action": "approve", "level": tc.level, "note": "已逐项核对原始材料与具体数字，证据完整"}
			mustStatus(t, request(s, "POST", path, in, admin), 200)
			mustStatus(t, request(s, "POST", path, in, admin), 409)
			var points, credits, stored int
			s.db.QueryRow("SELECT points,credits FROM users WHERE id=?", id).Scan(&points, &credits)
			s.db.QueryRow("SELECT credits FROM contribution_rewards WHERE gap_id=?", g).Scan(&stored)
			if points != tc.points || credits != tc.credits+10 || stored != tc.credits {
				t.Fatal(points, credits, stored)
			}
			// Editorial reclassification must not change the original award clawback.
			s.db.Exec("UPDATE gaps SET level='ordinary' WHERE id=?", g)
			mustStatus(t, request(s, "POST", path, map[string]string{"action": "dispute", "note": "核验发现关键证据不成立，撤销原奖励"}, admin), 200)
			s.db.QueryRow("SELECT points,credits FROM users WHERE id=?", id).Scan(&points, &credits)
			if points != 0 || credits != 10 {
				t.Fatal(points, credits)
			}
		})
	}
}
