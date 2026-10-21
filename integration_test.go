package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPaymentOriginExceptionIsExact(t *testing.T) {
	t.Setenv("PAY_PROVIDER", "")
	s := testServer(t)
	for _, check := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/billing/notify/huifu", 503},
		{"POST", "/api/billing/notify/huifu/", 403},
		{"POST", "/api/billing/notify/huifu/extra", 403},
		{"PUT", "/api/billing/notify/huifu", 403},
		{"POST", "/api/billing/orders", 403},
		{"POST", "/api/submissions", 403},
		{"POST", "/api/auth/verify", 403},
	} {
		r := httptest.NewRequest(check.method, "http://example.com"+check.path, strings.NewReader("sign=invalid&resp_data=invalid"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://gateway.example")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		if w.Code != check.want {
			t.Fatalf("%s %s: got %d want %d", check.method, check.path, w.Code, check.want)
		}
	}
	// The exception must not weaken the JSON requirement for ordinary same-origin writes.
	r := httptest.NewRequest("POST", "http://example.com/api/submissions", strings.NewReader("title=sample"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	mustStatus(t, w, 415)
}

func TestLatestSortUsesSiteReleaseAndUnknownSourceDateRemainsEmpty(t *testing.T) {
	s := testServer(t)
	_, member := account(t, s, "author@example.org", "member", 10)
	_, admin := account(t, s, founderEmail, "admin", 0)
	input := map[string]any{"title": "新的科研论文中文解读与实验证据", "summary": "这项实验比较了不同材料的性能，并给出可复核的测试条件和具体数据。", "body": strings.Repeat("正文描述实验过程、关键条件与研究限制，保留原始文献链接便于核查。", 5), "category": "物理", "level": "ordinary", "evidence": "official", "source_name": "实验机构", "source_url": "https://example.org/research", "tags": []string{"量子"}}
	w := request(s, "POST", "/api/submissions", input, member)
	mustStatus(t, w, 201)
	var source, created, release string
	if err := s.db.QueryRow("SELECT published_at,created_at,released_at FROM gaps WHERE source_url=?", input["source_url"]).Scan(&source, &created, &release); err != nil {
		t.Fatal(err)
	}
	if source != "" || release != "" || created != stamp(s.now()) {
		t.Fatalf("source or release date fabricated: %q %q %q", source, release, created)
	}
	oldSite := gap(t, s, "ordinary", "published", nil)
	newSite := gap(t, s, "ordinary", "published", nil)
	_, err := s.db.Exec("UPDATE gaps SET released_at=?,published_at=? WHERE id=?", stamp(s.now().Add(-4*time.Hour)), stamp(s.now()), oldSite)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec("UPDATE gaps SET released_at=?,published_at='2020-01-01T00:00:00Z' WHERE id=?", stamp(s.now().Add(-time.Hour)), newSite)
	if err != nil {
		t.Fatal(err)
	}
	w = request(s, "GET", "/api/gaps", nil, admin)
	mustStatus(t, w, 200)
	var list struct {
		Items []Gap `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.Items[0].ID != newSite || list.Items[1].ID != oldSite {
		t.Fatal("latest list did not use first site publication", w.Body.String())
	}
}
