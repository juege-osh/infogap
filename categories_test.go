package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLifestyleCategoriesAcrossSubmissionReviewFilterAndSubscription(t *testing.T) {
	for _, category := range []string{"生活实用", "省钱优惠", "招聘求职", "职业成长", "住房居住", "出行旅行", "商业创业"} {
		t.Run(category, func(t *testing.T) {
			s := testServer(t)
			_, author := account(t, s, "author@example.org", "member", 10)
			readerID, reader := account(t, s, "reader@example.org", "member", 10)
			_, admin := account(t, s, founderEmail, "admin", 0)
			if _, err := s.db.Exec("UPDATE users SET points=700 WHERE id=?", readerID); err != nil {
				t.Fatal(err)
			}
			input := map[string]any{
				"title":    category + "的公开信息与适用条件整理",
				"summary":  "这条公开信息说明具体适用条件、办理步骤和有效期限，读者应按自身情况核对。",
				"body":     strings.Repeat("正文保留公开材料的适用条件、时间和限制，不将个别案例当作普遍承诺。", 4),
				"category": category, "level": "ordinary", "evidence": "official",
				"source_name": "公开机构", "source_url": "https://example.org/notice", "tags": []string{"实用信息"},
			}
			w := request(s, "POST", "/api/submissions", input, author)
			mustStatus(t, w, 201)
			var created struct{ ID int64 }
			if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == 0 {
				t.Fatalf("invalid submission response: %s", w.Body.String())
			}
			edit := map[string]any{"title": input["title"], "summary": input["summary"], "body": input["body"], "category": category, "evidence": "official", "tags": input["tags"]}
			editPath := fmt.Sprintf("/api/admin/gaps/%d", created.ID)
			mustStatus(t, request(s, "POST", editPath, edit, author), 403)
			mustStatus(t, request(s, "POST", editPath, edit, admin), 200)
			mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/review/%d", created.ID), map[string]string{"action": "approve", "level": "ordinary"}, admin), 200)
			// The category test uses an already-visible publication; timing has its own suite.
			if _, err := s.db.Exec("UPDATE gaps SET released_at=? WHERE id=?", stamp(s.now().Add(-72*time.Hour)), created.ID); err != nil {
				t.Fatal(err)
			}
			gap(t, s, "ordinary", "published", nil) // unrelated physics item must stay out
			mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "category", "value": category}, reader), 201)
			for _, path := range []string{"/api/gaps?category=" + url.QueryEscape(category), "/api/gaps?view=subscribed"} {
				w = request(s, "GET", path, nil, reader)
				mustStatus(t, w, 200)
				var result struct {
					Items []Gap `json:"items"`
					Total int   `json:"total"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Total != 1 || len(result.Items) != 1 || result.Items[0].ID != created.ID || result.Items[0].Category != category || result.Items[0].Body != "" {
					t.Fatalf("category filter or locked content mismatch: %s", w.Body.String())
				}
			}
			edit["category"] = "不存在的分类"
			mustStatus(t, request(s, "POST", editPath, edit, admin), 400)
			input["category"] = "不存在的分类"
			mustStatus(t, request(s, "POST", "/api/submissions", input, author), 400)
			mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "category", "value": "不存在的分类"}, reader), 400)
		})
	}
}

func TestBootstrapKeepsOriginalCategoriesAndAddsLifestyleTopics(t *testing.T) {
	s := testServer(t)
	w := request(s, "GET", "/api/bootstrap", nil, "")
	mustStatus(t, w, 200)
	var result struct{ Categories []string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	expected := []string{"前沿科技", "人工智能", "生命科学", "物理", "化学", "地球与地理", "天文与空间", "材料科学", "开源技术", "军事与国防", "政治与公共政策", "农业与食品", "社会民生", "法律法规", "航空航天", "经济与产业", "能源与环境", "医疗健康", "教育与就业", "交通与基建", "公共安全", "生活实用", "省钱优惠", "招聘求职", "职业成长", "住房居住", "出行旅行", "商业创业"}
	if strings.Join(result.Categories, "|") != strings.Join(expected, "|") {
		t.Fatalf("category catalog changed unexpectedly: %v", result.Categories)
	}
}
