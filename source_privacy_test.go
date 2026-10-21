package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func assertNoPrivateSource(t *testing.T, response string) {
	t.Helper()
	for _, marker := range []string{"linux.do", "linux do", "linuxdo", "私密科技社", "private-note"} {
		if strings.Contains(strings.ToLower(response), marker) {
			t.Fatalf("internal source leaked: %s", response)
		}
	}
}

func TestInternalSourceHiddenAcrossMemberResponsesAndSearch(t *testing.T) {
	s := testServer(t)
	authorID, author := account(t, s, "author@example.org", "member", 10)
	readerID, reader := account(t, s, "reader@example.org", "member", 10)
	_, admin := account(t, s, founderEmail, "admin", 0)
	_, reviewer := account(t, s, "reviewer@example.org", "reviewer", 0)
	g := gap(t, s, "ordinary", "published", authorID)
	link := "https://linux.do/t/topic/123"
	body := "来自 LINUX DO 与 LinuxDO、私密科技社的线索。[讨论](https://www.linux.do/t/topic/123) 与 https://linux.do/t/topic/123\n[论文](https://www.nature.com/articles/public-evidence)\n[私源]: https://linux.do/t/topic/123\n编码链接 https://%6cinux.do/t/topic/123；实体 linux&#46;do；零宽 li\u200bnux.do；无协议链接 linux.do/t/topic/123"
	_, err := s.db.Exec("UPDATE gaps SET title='LinuxDO 的科研进展标题',summary='私密科技社与 LINUX DO 的公开预览摘要',body=?,tags='[\"LINUX DO\",\"量子\"]',source_name='私密科技社',source_url=?,source_visibility='internal',review_note='private-note: linux.do' WHERE id=?", body, link, g)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/gaps/%d", g)
	w := request(s, "POST", path+"/unlock", nil, reader)
	mustStatus(t, w, 200)
	assertNoPrivateSource(t, w.Body.String())
	if strings.Contains(w.Body.String(), "/t/topic/123") {
		t.Fatal("private URL path remained", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "https://www.nature.com/articles/public-evidence") {
		t.Fatal("public evidence link removed")
	}
	mustStatus(t, request(s, "POST", path+"/save", nil, reader), 200)
	for _, cookie := range []string{"", author, reader} {
		for _, p := range []string{"/api/gaps", path} {
			w = request(s, "GET", p, nil, cookie)
			mustStatus(t, w, 200)
			assertNoPrivateSource(t, w.Body.String())
			if !strings.Contains(w.Body.String(), `"source_visibility":"internal"`) || !strings.Contains(w.Body.String(), `"source_name":""`) || !strings.Contains(w.Body.String(), `"source_url":""`) {
				t.Fatal("internal attribution fields not redacted", w.Body.String())
			}
		}
		for _, term := range []string{"linux.do", "LINUX DO", "LinuxDO", "私密科技社"} {
			w = request(s, "GET", "/api/gaps?q="+url.QueryEscape(term), nil, cookie)
			mustStatus(t, w, 200)
			if !strings.Contains(w.Body.String(), `"total":0`) {
				t.Fatal("search inferred an internal source", w.Body.String())
			}
		}
	}
	for _, p := range []string{"/api/gaps?view=saved", "/api/gaps?view=unlocked", "/api/me/activity"} {
		w = request(s, "GET", p, nil, reader)
		mustStatus(t, w, 200)
		assertNoPrivateSource(t, w.Body.String())
	}
	for _, p := range []string{"/api/gaps?view=mine", "/api/me/activity"} {
		w = request(s, "GET", p, nil, author)
		mustStatus(t, w, 200)
		assertNoPrivateSource(t, w.Body.String())
	}
	_, _ = s.db.Exec("UPDATE users SET points=700 WHERE id=?", readerID)
	mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "tag", "value": "LINUX DO"}, reader), 201)
	w = request(s, "GET", "/api/gaps?view=subscribed", nil, reader)
	if !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatal("subscription inferred hidden tag", w.Body.String())
	}
	mustStatus(t, request(s, "POST", "/api/subscriptions", map[string]string{"kind": "tag", "value": "量子"}, reader), 201)
	w = request(s, "GET", "/api/gaps?view=subscribed", nil, reader)
	mustStatus(t, w, 200)
	assertNoPrivateSource(t, w.Body.String())
	if !strings.Contains(w.Body.String(), `"total":1`) {
		t.Fatal("non-sensitive subscription lost")
	}
	for _, cookie := range []string{admin, reviewer} {
		w = request(s, "GET", path, nil, cookie)
		mustStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), link) || !strings.Contains(w.Body.String(), "private-note") {
			t.Fatal("staff lost traceable source", w.Body.String())
		}
		w = request(s, "GET", "/api/gaps?q=LinuxDO", nil, cookie)
		if !strings.Contains(w.Body.String(), `"total":1`) {
			t.Fatal("staff search lost original text")
		}
	}
	_, _ = s.db.Exec("UPDATE gaps SET status='disputed' WHERE id=?", g)
	w = request(s, "GET", path, nil, reader)
	mustStatus(t, w, 200)
	assertNoPrivateSource(t, w.Body.String())
	// Source information remains in storage, including when the author reads their own item.
	var original string
	_ = s.db.QueryRow("SELECT source_url FROM gaps WHERE id=?", g).Scan(&original)
	if original != link {
		t.Fatal("redaction altered provenance in database")
	}
}

func TestInternalApprovalRejectsAliasesThenRetainsUnverifiedEvidence(t *testing.T) {
	s := testServer(t)
	author, _ := account(t, s, "author@example.org", "member", 10)
	_, admin := account(t, s, founderEmail, "admin", 0)
	g := gap(t, s, "ordinary", "pending", author)
	_, _ = s.db.Exec("UPDATE gaps SET source_visibility='internal',source_name='私密科技社',source_url='https://linux.do/t/123',evidence='unverified' WHERE id=?", g)
	path := fmt.Sprintf("/api/admin/review/%d", g)
	for _, alias := range []string{"linux.do", "www.linux.do", "LINUX DO", "LinuxDO", "私密科技社", "https://%6cinux.do/t/123", "linux&#46;do", "li\u200bnux.do"} {
		for _, field := range []string{"title", "summary", "body", "tags"} {
			_, _ = s.db.Exec("UPDATE gaps SET title='可复核的科研进展标题',summary='整理后的科研进展摘要，原始事实仍有待核验',body='客观描述研究进展和限制',tags='[]' WHERE id=?", g)
			value := alias
			if field == "tags" {
				b, _ := json.Marshal([]string{alias})
				value = string(b)
			}
			_, _ = s.db.Exec("UPDATE gaps SET "+field+"=? WHERE id=?", value, g)
			mustStatus(t, request(s, "POST", path, map[string]string{"action": "approve", "level": "ordinary"}, admin), 400)
		}
	}
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM contribution_rewards WHERE gap_id=?", g).Scan(&n)
	if n != 0 {
		t.Fatal("rejected internal approval granted reward")
	}
	clean := map[string]any{"title": "科研团队分享一种可复核的新方法", "summary": "这条社区线索概述了一种实验方法，目前需要根据原始材料进一步核验。", "body": strings.Repeat("方法的适用范围和实验条件仍需核验，现有描述不能单独作为已证实结论。", 4) + "\n[公开论文](https://www.nature.com/articles/public-evidence)", "category": "物理", "evidence": "unverified", "tags": []string{"量子"}, "source_visibility": "internal"}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/gaps/%d", g), clean, admin), 200)
	mustStatus(t, request(s, "POST", path, map[string]string{"action": "approve", "level": "ordinary", "note": "内部核对 private-note linux.do"}, admin), 200)
	var evidence, source, visibility string
	_ = s.db.QueryRow("SELECT evidence,source_url,source_visibility FROM gaps WHERE id=?", g).Scan(&evidence, &source, &visibility)
	if evidence != "unverified" || source != "https://linux.do/t/123" || visibility != "internal" {
		t.Fatal("editorial processing changed evidence/provenance", evidence, source, visibility)
	}
}

func TestPrivateSubmissionDefaultAndPublicSourcePreserved(t *testing.T) {
	s := testServer(t)
	_, member := account(t, s, "author@example.org", "member", 10)
	for i, link := range []string{"https://Linux.Do/t/123", "https://www.linux.do/t/456", "https://www.nature.com/articles/123"} {
		in := map[string]any{"title": fmt.Sprintf("研究进展的文本摘要与可复核信息%d", i), "summary": "研究提出一项新方法，但适用条件与局限仍需根据完整材料核验。", "body": strings.Repeat("这里只保存研究事实和可以独立检查的条件，不能据此推导未获支持的结论。", 4), "category": "物理", "source_name": "科研来源", "source_url": link, "level": "ordinary", "evidence": "official", "tags": []string{"研究"}}
		w := request(s, "POST", "/api/submissions", in, member)
		mustStatus(t, w, 201)
		var visibility, evidence, status string
		_ = s.db.QueryRow("SELECT source_visibility,evidence,status FROM gaps WHERE source_url=?", link).Scan(&visibility, &evidence, &status)
		if status != "pending" {
			t.Fatal("submission unexpectedly published")
		}
		if i < 2 && (visibility != "internal" || evidence != "unverified") {
			t.Fatal("community submission not internal/unverified")
		}
		if i == 2 && (visibility != "public" || evidence != "official") {
			t.Fatal("public source changed")
		}
	}
	w := request(s, "GET", "/api/me/activity", nil, member)
	assertNoPrivateSource(t, w.Body.String())
	if !strings.Contains(w.Body.String(), "https://www.nature.com/articles/123") {
		t.Fatal("public source attribution removed")
	}
}

func TestSourceVisibilityMigrationAndEditAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	s, err := newServer(path, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	private := gap(t, s, "ordinary", "published", nil)
	public := gap(t, s, "ordinary", "published", nil)
	_, _ = s.db.Exec("UPDATE gaps SET source_url='https://linux.do/t/123' WHERE id=?", private)
	if _, err = s.db.Exec("ALTER TABLE gaps DROP COLUMN source_visibility"); err != nil {
		t.Fatal(err)
	}
	s.db.Close()
	s, err = newServer(path, "web", "http://example.com")
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	for id, want := range map[int64]string{private: "internal", public: "public"} {
		var got string
		_ = s.db.QueryRow("SELECT source_visibility FROM gaps WHERE id=?", id).Scan(&got)
		if got != want {
			t.Fatalf("migration: %d got %s want %s", id, got, want)
		}
	}
	_, admin := account(t, s, founderEmail, "admin", 0)
	input := map[string]any{"title": "核验后的新研究进展与适用条件", "summary": "这项研究的实验条件与适用范围已经重新整理，仍需保留证据边界。", "body": strings.Repeat("科研事实需要根据实验结果独立核验，文本将保留关键方法与限制。", 4), "category": "物理", "evidence": "unverified", "tags": []string{"研究"}, "source_visibility": "internal"}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/gaps/%d", public), input, admin), 200)
	var note, visibility, source string
	_ = s.db.QueryRow("SELECT note FROM audit WHERE action='edit_gap' ORDER BY id DESC LIMIT 1").Scan(&note)
	_ = s.db.QueryRow("SELECT source_visibility,source_url FROM gaps WHERE id=?", public).Scan(&visibility, &source)
	if !strings.Contains(note, `"source_visibility":"public"`) || visibility != "internal" || !strings.Contains(source, "nature.com") {
		t.Fatal("edit lost source visibility audit/provenance")
	}
	input["source_visibility"] = "invalid"
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/gaps/%d", public), input, admin), 400)
	delete(input, "source_visibility")
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/gaps/%d", public), input, admin), 200)
	_ = s.db.QueryRow("SELECT source_visibility FROM gaps WHERE id=?", public).Scan(&visibility)
	if visibility != "internal" {
		t.Fatal("omitted visibility reset existing privacy")
	}
}
