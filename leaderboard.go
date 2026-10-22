package main

import (
	"net/http"
	"strconv"
)

type leaderboardEntry struct {
	ID            int64  `json:"id"`
	Username      string `json:"username"`
	Points        int    `json:"points"`
	Contributions int    `json:"contributions"`
	Quality       int    `json:"quality"`
	TierIndex     int    `json:"tier_index"`
	Rank          int    `json:"rank"`
}

func (s *server) leaderboard(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	filter := -1
	if v := r.URL.Query().Get("tier"); v != "" && v != "all" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 0 || n >= len(tiers) {
			fail(w, 400, "段位筛选无效")
			return
		}
		filter = n
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	rows, err := s.db.Query(`SELECT u.id,u.username,u.points,COUNT(cr.gap_id),COALESCE(SUM(CASE WHEN cr.featured>0 OR cr.rare>0 THEN 1 ELSE 0 END),0) FROM users u LEFT JOIN contribution_rewards cr ON cr.user_id=u.id AND cr.revoked=0 WHERE u.disabled=0 AND u.role!='admin' AND u.id NOT IN (SELECT user_id FROM test_accounts) GROUP BY u.id ORDER BY u.points DESC,5 DESC,4 DESC,u.id ASC`)
	if err != nil {
		fail(w, 500, "排行榜暂不可用")
		return
	}
	defer rows.Close()
	entries := []leaderboardEntry{}
	var self *leaderboardEntry
	for rows.Next() {
		var e leaderboardEntry
		if err := rows.Scan(&e.ID, &e.Username, &e.Points, &e.Contributions, &e.Quality); err != nil {
			fail(w, 500, "排行榜读取失败")
			return
		}
		for i, t := range tiers {
			if e.Points >= t.MinPoints {
				e.TierIndex = i
			}
		}
		if filter >= 0 && e.TierIndex != filter {
			continue
		}
		e.Rank = len(entries) + 1
		entries = append(entries, e)
		if e.ID == u.ID {
			copy := e
			self = &copy
		}
	}
	if rows.Err() != nil {
		fail(w, 500, "排行榜读取失败")
		return
	}
	total := len(entries)
	pages := (total + 19) / 20
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * 20
	end := start + 20
	if end > total {
		end = total
	}
	send(w, 200, map[string]any{"items": entries[start:end], "total": total, "page": page, "pages": pages, "self": self, "tier": filter})
}
