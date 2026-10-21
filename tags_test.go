package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSingleCategoryAndTenTagsForSubmissionAndEditing(t *testing.T) {
	s := testServer(t)
	_, author := account(t, s, "author@example.org", "member", 10)
	_, admin := account(t, s, founderEmail, "admin", 0)
	tags := []string{"国家:日本", "地区:东亚", "旅行:优惠渠道"}
	for i := 4; i <= 10; i++ {
		tags = append(tags, fmt.Sprintf("实用标签%d", i))
	}
	input := map[string]any{
		"title":    "日本旅行优惠渠道的公开信息整理",
		"summary":  "说明优惠渠道的适用人群、费用和开放时间，读者应按自身情况核对。",
		"body":     strings.Repeat("正文保留公开材料的适用条件、时间和限制，不将个别案例当作普遍承诺。", 4),
		"category": "出行旅行", "level": "ordinary", "evidence": "official", "tags": tags,
		"source_name": "公开机构", "source_url": "https://example.org/travel",
	}
	w := request(s, "POST", "/api/submissions", input, author)
	mustStatus(t, w, 201)
	var created struct{ ID int64 }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == 0 {
		t.Fatalf("invalid submission response: %s", w.Body.String())
	}
	edit := map[string]any{"title": input["title"], "summary": input["summary"], "body": input["body"], "category": input["category"], "evidence": input["evidence"], "tags": tags}
	editPath := fmt.Sprintf("/api/admin/gaps/%d", created.ID)
	mustStatus(t, request(s, "POST", editPath, edit, admin), 200)
	var storedTags, storedCategory string
	if err := s.db.QueryRow("SELECT tags,category FROM gaps WHERE id=?", created.ID).Scan(&storedTags, &storedCategory); err != nil {
		t.Fatal(err)
	}
	var saved []string
	if err := json.Unmarshal([]byte(storedTags), &saved); err != nil || len(saved) != 10 || storedCategory != "出行旅行" {
		t.Fatalf("ten tags or single category lost: %s %q", storedTags, storedCategory)
	}
	input["tags"], edit["tags"] = append(tags, "第十一个标签"), append(tags, "第十一个标签")
	mustStatus(t, request(s, "POST", "/api/submissions", input, author), 400)
	mustStatus(t, request(s, "POST", editPath, edit, admin), 400)
	input["tags"], edit["tags"] = tags, tags
	for _, invalid := range []any{[]string{"出行旅行"}, []string{"出行旅行", "省钱优惠"}, "出行旅行,省钱优惠", "出行旅行、省钱优惠"} {
		input["category"], edit["category"] = invalid, invalid
		mustStatus(t, request(s, "POST", "/api/submissions", input, author), 400)
		mustStatus(t, request(s, "POST", editPath, edit, admin), 400)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM gaps").Scan(&count); err != nil || count != 1 {
		t.Fatalf("invalid inputs created content: %d %v", count, err)
	}
	var afterTags, afterCategory string
	if err := s.db.QueryRow("SELECT tags,category FROM gaps WHERE id=?", created.ID).Scan(&afterTags, &afterCategory); err != nil || afterTags != storedTags || afterCategory != storedCategory {
		t.Fatal("rejected edit changed stored content")
	}
}
