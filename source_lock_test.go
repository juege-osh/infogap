package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func assertLockedPublicSource(t *testing.T, response string) {
	t.Helper()
	for _, marker := range []string{"atlassource", "research.atlas.example", "original-hidden-123", "source-review-private", "https://", "evidence.example", "/public"} {
		if strings.Contains(strings.ToLower(response), marker) {
			t.Fatalf("locked source leaked %q: %s", marker, response)
		}
	}
}

func TestLockedSourcePublicAPIAndAuthorizedRecovery(t *testing.T) {
	s := testServer(t)
	authorID, author := account(t, s, "source-author@example.org", "member", 100)
	_, reader := account(t, s, "source-reader@example.org", "member", 100)
	_, other := account(t, s, "source-other@example.org", "member", 100)
	_, admin := account(t, s, founderEmail, "admin", 0)
	_, reviewer := account(t, s, "source-reviewer@example.org", "reviewer", 0)
	id := gap(t, s, "ordinary", "published", authorID)
	link := "https://research.atlas.example/original-hidden-123"
	title := "AtlasSource 公布研究进展 research.atlas.example"
	summary := "AtlasSource 的公开摘要：[原始资料](" + link + ")，编码网址 https://%72esearch.atlas.example/original-hidden-123；[无关证据](https://evidence.example/public)"
	_, err := s.db.Exec("UPDATE gaps SET title=?,summary=?,body=?,category='出行旅行',tags='[\"AtlasSource\",\"国家:AtlasSource\",\"研究\"]',source_name='AtlasSource',source_url=?,source_visibility='public',review_note='source-review-private' WHERE id=?", title, summary, "原始全文 AtlasSource "+link, link, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE users SET username='AtlasSource 作者' WHERE id=?", authorID); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/gaps/%d", id)
	for _, cookie := range []string{"", reader, other} {
		for _, p := range []string{"/api/gaps", path} {
			w := request(s, "GET", p, nil, cookie)
			mustStatus(t, w, 200)
			assertLockedPublicSource(t, w.Body.String())
			if !strings.Contains(w.Body.String(), `"source_name":""`) || !strings.Contains(w.Body.String(), `"source_url":""`) || !strings.Contains(w.Body.String(), "来源已锁定") {
				t.Fatal("locked source fields not cleared", w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "无关证据") {
				t.Fatal("evidence anchor text removed", w.Body.String())
			}
		}
		for _, term := range []string{"AtlasSource", "research.atlas.example", "original-hidden-123", "evidence.example", "/public"} {
			w := request(s, "GET", "/api/gaps?q="+url.QueryEscape(term), nil, cookie)
			mustStatus(t, w, 200)
			if !strings.Contains(w.Body.String(), `"total":0`) {
				t.Fatal("search inferred locked source", w.Body.String())
			}
		}
		for _, p := range []string{"/api/travel/facets", "/api/gaps?country=AtlasSource"} {
			w := request(s, "GET", p, nil, cookie)
			mustStatus(t, w, 200)
			assertLockedPublicSource(t, w.Body.String())
			if p != "/api/travel/facets" && !strings.Contains(w.Body.String(), `"total":0`) {
				t.Fatal("travel filter inferred locked source", w.Body.String())
			}
		}
	}
	mustStatus(t, request(s, "POST", path+"/save", nil, reader), 200)
	for _, p := range []string{"/api/gaps?view=saved", "/api/me/activity"} {
		w := request(s, "GET", p, nil, reader)
		mustStatus(t, w, 200)
		assertLockedPublicSource(t, w.Body.String())
	}
	mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "tag", "value": "AtlasSource"}, reader), 403)
	w := request(s, "GET", "/api/gaps?view=subscribed", nil, reader)
	mustStatus(t, w, 403)
	// Disputed status previously exposed review notes; source notes stay locked.
	_, _ = s.db.Exec("UPDATE gaps SET status='disputed' WHERE id=?", id)
	w = request(s, "GET", path, nil, reader)
	mustStatus(t, w, 200)
	assertLockedPublicSource(t, w.Body.String())
	_, _ = s.db.Exec("UPDATE gaps SET status='published' WHERE id=?", id)
	mustStatus(t, request(s, "POST", path+"/unlock", nil, reader), 200)
	for _, cookie := range []string{reader, author, admin, reviewer} {
		w = request(s, "GET", path, nil, cookie)
		mustStatus(t, w, 200)
		var g Gap
		if err = json.Unmarshal(w.Body.Bytes(), &g); err != nil {
			t.Fatal(err)
		}
		if !g.Unlocked || g.SourceName != "AtlasSource" || g.SourceURL != link || g.Title != title || g.Summary != summary || g.Body == "" || g.Author != "AtlasSource 作者" {
			t.Fatal("authorized reader lost original source", w.Body.String())
		}
		for _, p := range []string{"/api/gaps?q=AtlasSource", "/api/gaps?country=AtlasSource"} {
			w = request(s, "GET", p, nil, cookie)
			mustStatus(t, w, 200)
			if !strings.Contains(w.Body.String(), `"total":1`) {
				t.Fatal("authorized search did not recover source", w.Body.String())
			}
		}
		w = request(s, "GET", "/api/travel/facets", nil, cookie)
		mustStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), "AtlasSource") {
			t.Fatal("authorized facet lost source tag")
		}
	}
	w = request(s, "GET", "/api/gaps?view=subscribed", nil, reader)
	mustStatus(t, w, 403)
	// One user's unlock must not affect another user's search or response.
	w = request(s, "GET", path, nil, other)
	mustStatus(t, w, 200)
	assertLockedPublicSource(t, w.Body.String())
	w = request(s, "GET", "/api/gaps?q=AtlasSource", nil, other)
	mustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatal("another user's unlock disclosed source")
	}
	var storedURL, storedTitle string
	if err = s.db.QueryRow("SELECT source_url,title FROM gaps WHERE id=?", id).Scan(&storedURL, &storedTitle); err != nil || storedURL != link || storedTitle != title {
		t.Fatal("redaction modified stored provenance", err)
	}
}

func TestLockedSourceNormalizationAndInternalPriority(t *testing.T) {
	for _, text := range []string{
		"AtlasSource research.atlas.example",
		"[AtlasSource](https://research.atlas.example/original-hidden-123)",
		"[ref]: https://research.atlas.example/original-hidden-123",
		"https://%72esearch.atlas.example/original-hidden-123",
		"research&#46;atlas.example resea\u200brch.atlas.example",
		"research.atlas.example/original-hidden-123",
		"[无关证据](https://evidence.example/public) 和 https://evidence.example/public",
		"[evidence]: https://evidence.example/public",
		"https://evi%64ence.example/public evidence.example/public evidence.example",
	} {
		g := &Gap{Title: text, Summary: text, Author: text, Tags: []string{text}, SourceName: "AtlasSource", SourceURL: "https://research.atlas.example/original-hidden-123", SourceVisibility: "public", ReviewNote: "source-review-private"}
		redactLockedSource(g)
		b, _ := json.Marshal(g)
		assertLockedPublicSource(t, string(b))
	}
	redactLockedSource(nil)
	// Even an unlocked member cannot recover internal attribution; staff access
	// is covered by the existing API privacy tests run alongside this suite.
	g := &Gap{Title: "LinuxDO 线索", Body: "LINUX DO https://linux.do/t/topic/123", SourceName: "LinuxDO", SourceURL: "https://linux.do/t/topic/123", SourceVisibility: "internal", Unlocked: true}
	redactInternalSource(g)
	redactLockedSource(g)
	b, _ := json.Marshal(g)
	assertNoPrivateSource(t, string(b))
}

func TestLockedSourceInternalAuxiliaryLinksWaitForUnlock(t *testing.T) {
	s := testServer(t)
	_, reader := account(t, s, "internal-link-reader@example.org", "member", 100)
	id := gap(t, s, "ordinary", "published", nil)
	_, err := s.db.Exec("UPDATE gaps SET source_visibility='internal',source_name='LinuxDO',source_url='https://linux.do/t/123',summary='LinuxDO 线索：[公开论文](https://evidence.example/public)',tags='[\"evidence.example/public\"]',body='LinuxDO 讨论与公开论文 https://evidence.example/public' WHERE id=?", id)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/gaps/%d", id)
	for _, cookie := range []string{"", reader} {
		w := request(s, "GET", path, nil, cookie)
		mustStatus(t, w, 200)
		assertNoPrivateSource(t, w.Body.String())
		assertLockedPublicSource(t, w.Body.String())
		w = request(s, "GET", "/api/gaps?q=evidence.example", nil, cookie)
		mustStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), `"total":0`) {
			t.Fatal("internal preview search inferred auxiliary evidence", w.Body.String())
		}
	}
	w := request(s, "POST", path+"/unlock", nil, reader)
	mustStatus(t, w, 200)
	assertNoPrivateSource(t, w.Body.String())
	if !strings.Contains(w.Body.String(), "https://evidence.example/public") {
		t.Fatal("unlocked internal item lost public evidence")
	}
	w = request(s, "GET", "/api/gaps?q=evidence.example", nil, reader)
	mustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"total":1`) {
		t.Fatal("unlocked internal public evidence was not searchable")
	}
}
