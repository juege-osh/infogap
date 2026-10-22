package main

import (
	"net/http"
	"strconv"
	"strings"
)

type readingNeighbor struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
}

func (s *server) readingNeighbors(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	current, e := s.oneGap(idParam(r), u)
	if e != nil || !s.canSee(current, u) {
		fail(w, 404, "信息不存在")
		return
	}
	seen := map[int64]bool{current.ID: true}
	parts := strings.Split(r.URL.Query().Get("visited"), ",")
	if len(parts) > 2000 {
		fail(w, 400, "阅读记录过长，请重新开始本轮阅读")
		return
	}
	for _, v := range parts {
		if v == "" {
			continue
		}
		id, e := strconv.ParseInt(v, 10, 64)
		if e != nil || id < 1 {
			fail(w, 400, "阅读记录无效")
			return
		}
		seen[id] = true
	}
	vis, args := s.visibilityClause(u)
	args = append([]any{u.ID}, args...)
	rows, e := s.db.Query(`SELECT g.id,g.title,g.category,EXISTS(SELECT 1 FROM unlocks un WHERE un.user_id=? AND un.gap_id=g.id) FROM gaps g WHERE g.status='published'`+vis+` ORDER BY g.released_at DESC,g.id DESC`, args...)
	if e != nil {
		fail(w, 500, "读取相邻情报失败")
		return
	}
	type candidate struct {
		item     readingNeighbor
		unlocked bool
	}
	all := []candidate{}
	for rows.Next() {
		var c candidate
		if rows.Scan(&c.item.ID, &c.item.Title, &c.item.Category, &c.unlocked) != nil {
			rows.Close()
			fail(w, 500, "读取失败")
			return
		}
		all = append(all, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	var prev, next *readingNeighbor
	currentAt := -1
	for i, c := range all {
		if c.item.ID == current.ID {
			currentAt = i
			break
		}
	}
	if currentAt >= 0 {
		for i := currentAt - 1; i >= 0; i-- {
			if all[i].item.Category == current.Category {
				v := all[i].item
				prev = &v
				break
			}
		}
		for i := currentAt + 1; i < len(all); i++ {
			v := all[i].item
			if v.Category == current.Category && !seen[v.ID] {
				next = &v
				break
			}
		}
	}
	transition, message := "same", ""
	subs := map[string]bool{}
	if next == nil && u.Entitlements.Subscriptions {
		rows, e = s.db.Query("SELECT value FROM subscriptions WHERE user_id=? AND kind='category' ORDER BY id LIMIT ?", u.ID, u.Entitlements.SubscriptionLimit)
		if e != nil {
			fail(w, 500, "订阅读取失败")
			return
		}
		for rows.Next() {
			var c string
			if rows.Scan(&c) == nil {
				subs[c] = true
			}
		}
		rows.Close()
		for _, c := range all {
			v := c.item
			if v.Category != current.Category && subs[v.Category] && !seen[v.ID] && !c.unlocked {
				next = &v
				transition = "subscribed"
				message = "当前主题信息差已刷完，将切换到另一订阅主题：" + v.Category
				break
			}
		}
	}
	if next == nil {
		candidates := []readingNeighbor{}
		for _, c := range all {
			v := c.item
			if v.Category != current.Category && !seen[v.ID] && !c.unlocked {
				candidates = append(candidates, v)
			}
		}
		if len(candidates) > 0 {
			v := candidates[int(s.now().UnixNano()%int64(len(candidates)))]
			next = &v
			transition = "random"
			message = "当前主题信息差已刷完，没有其他可继续阅读的订阅主题，即将切换到随机主题：" + v.Category
		} else {
			transition = "end"
			message = "当前可阅读的信息差已刷完，稍后再来看看新内容。"
		}
	}
	send(w, 200, map[string]any{"previous": prev, "next": next, "next_transition": transition, "next_message": message})
}
