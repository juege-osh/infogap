package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGapCodeFormatAndRecognition(t *testing.T) {
	for _, tc := range []struct {
		id       int64
		level    string
		category string
		code     string
	}{{1, "ordinary", "物理", "PT-WL-01"}, {116, "featured", "人工智能", "JX-AI-116"}, {8, "rare", "物理", "XY-WL-08"}, {4, "exclusive", "化学", "DJ-HX-04"}, {205, "ordinary", "出行旅行", "PT-LX-205"}, {1234, "exclusive", "商业创业", "DJ-CY-1234"}} {
		if got := gapCode(tc.id, tc.level, tc.category); got != tc.code {
			t.Fatalf("code=%s want=%s", got, tc.code)
		}
		for _, input := range []string{tc.code, strings.ToLower(tc.code), " " + tc.code + " "} {
			if id, ok := gapCodeID(input); !ok || id != tc.id {
				t.Fatalf("unrecognized code %q: %d %v", input, id, ok)
			}
		}
	}
	for _, legacy := range []struct {
		code string
		id   int64
	}{{"PT01", 1}, {"jx116", 116}, {"XY08", 8}, {" dj205 ", 205}} {
		if id, ok := gapCodeID(legacy.code); !ok || id != legacy.id {
			t.Fatalf("legacy code stopped working: %q", legacy.code)
		}
	}
	for _, invalid := range []string{"", "01", "100", "PT", "PT1", "PT00", "PT-1", "PT-01", "PT+1", "PT０１", "PT01suffix", "XY 03", "AB01", "PT99999999999999999999", "PT01' OR 1=1", "PT-XX-01", "AB-AI-01", "PT-AI-1", "PT-AI-00", "PT-AI--01", "PT-AI-+01", "PT-AI-０１", "PT-AI-01-more", "PT-AI-99999999999999999999"} {
		if _, ok := gapCodeID(invalid); ok {
			t.Fatalf("accepted non-code %q", invalid)
		}
	}
}

func TestGapTopicCodesCoverAllCategoriesUniquely(t *testing.T) {
	s := testServer(t)
	w := request(s, "GET", "/api/bootstrap", nil, "")
	mustStatus(t, w, 200)
	var boot struct {
		Categories []string
		TopicCodes map[string]string `json:"topic_codes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if len(boot.Categories) != 28 || len(boot.TopicCodes) != 28 {
		t.Fatal("topic code catalog is incomplete")
	}
	seen := map[string]bool{}
	for _, category := range boot.Categories {
		code := boot.TopicCodes[category]
		if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' || seen[code] {
			t.Fatalf("missing, invalid or duplicate topic code for %s: %s", category, code)
		}
		seen[code] = true
		formatted := gapCode(9, "ordinary", category)
		if formatted != "PT-"+code+"-09" {
			t.Fatal("bootstrap and formatter disagree", category)
		}
		if id, ok := gapCodeID(formatted); !ok || id != 9 {
			t.Fatal("known topic code is not searchable", formatted)
		}
	}
}

func TestGapCodesAreGlobalAndSharedAcrossResponses(t *testing.T) {
	s := testServer(t)
	authorID, author := account(t, s, "author@example.org", "member", 10)
	_, admin := account(t, s, founderEmail, "admin", 0)
	want := map[int64]string{}
	for i, level := range []string{"ordinary", "featured", "rare", "exclusive"} {
		id := gap(t, s, level, "published", authorID)
		if id != int64(i+1) {
			t.Fatal("levels did not share a single sequence")
		}
		want[id] = []string{"PT-WL-01", "JX-WL-02", "XY-WL-03", "DJ-WL-04"}[i]
	}
	// Exercise the actual API at three digits, not just the formatter.
	if _, err := s.db.Exec("UPDATE sqlite_sequence SET seq=99 WHERE name='gaps'"); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"title": "全球唯一编号的待审投稿测试", "summary": "公开来源的具体信息摘要，保留适用条件和证据范围。",
		"body":     strings.Repeat("本文保留官方信息的具体条件、日期和局限，供成员按实际情况核实。", 4),
		"category": "物理", "level": "ordinary", "evidence": "official", "source_name": "公开机构", "source_url": "https://example.org/code-test", "tags": []string{},
	}
	w := request(s, "POST", "/api/submissions", input, author)
	mustStatus(t, w, 201)
	var created struct {
		ID     int64
		Code   string
		Status string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID != 100 || created.Code != "PT-WL-100" || created.Status != "pending" {
		t.Fatal("submission code mismatch", w.Body.String())
	}
	want[created.ID] = created.Code
	check := func(items []Gap, count int) {
		t.Helper()
		if len(items) != count {
			t.Fatalf("got %d records, want %d", len(items), count)
		}
		for _, g := range items {
			if g.Code == "" || g.Code != want[g.ID] {
				t.Fatalf("inconsistent code for %d: %s", g.ID, g.Code)
			}
		}
	}
	for _, path := range []string{"/api/gaps?view=mine", "/api/me/activity", "/api/admin/overview"} {
		cookie := author
		if path == "/api/admin/overview" {
			cookie = admin
		}
		w = request(s, "GET", path, nil, cookie)
		mustStatus(t, w, 200)
		var result struct{ Items, Submissions, Content, Queue []Gap }
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		switch path {
		case "/api/gaps?view=mine":
			check(result.Items, 5)
		case "/api/me/activity":
			check(result.Submissions, 5)
		default:
			check(result.Content, 5)
			check(result.Queue, 1)
		}
	}
	for id, code := range want {
		w = request(s, "GET", fmt.Sprintf("/api/gaps/%d", id), nil, author)
		mustStatus(t, w, 200)
		var g Gap
		if err := json.Unmarshal(w.Body.Bytes(), &g); err != nil || g.Code != code {
			t.Fatal("detail code mismatch", w.Body.String())
		}
	}
	// SQLite AUTOINCREMENT must not reuse a committed code even after deletion.
	if _, err := s.db.Exec("DELETE FROM gaps WHERE id=100"); err != nil {
		t.Fatal(err)
	}
	if next := gap(t, s, "rare", "pending", nil); next != 101 {
		t.Fatalf("deleted code was reused: %d", next)
	}
}

func codeSearch(t *testing.T, s *server, query, cookie string) []Gap {
	t.Helper()
	w := request(s, "GET", "/api/gaps?"+query, nil, cookie)
	mustStatus(t, w, 200)
	var result struct {
		Items []Gap
		Total int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Total != len(result.Items) {
		t.Fatal("invalid code search response", w.Body.String())
	}
	return result.Items
}

func TestGapCodeSearchPreservesAuthorizationAndFilters(t *testing.T) {
	s := testServer(t)
	readerID, reader := account(t, s, "reader@example.org", "member", 10)
	authorID, author := account(t, s, "author@example.org", "member", 10)
	_, admin := account(t, s, founderEmail, "admin", 0)
	if _, err := s.db.Exec("UPDATE users SET membership_until=? WHERE id=?", stamp(s.now().Add(time.Hour)), readerID); err != nil {
		t.Fatal(err)
	}
	id := gap(t, s, "ordinary", "published", nil)
	other := gap(t, s, "ordinary", "published", nil)
	if _, err := s.db.Exec("UPDATE gaps SET title='PT01 是另一条正文标题中的字符，不能冒充编号' WHERE id=?", other); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []string{"", reader} {
		for _, query := range []string{"q=pt01", "q=pt-wl-01", "q=PT-WL-01"} {
			items := codeSearch(t, s, query, cookie)
			if len(items) != 1 || items[0].ID != id || items[0].Code != "PT-WL-01" || items[0].Body != "" || items[0].Unlocked {
				t.Fatal("code search was not exact or exposed locked text", items)
			}
		}
		items := codeSearch(t, s, "q=01", cookie)
		if len(items) != 1 || items[0].ID != other {
			t.Fatal("numeric text query was incorrectly treated as an ID", items)
		}
	}
	for _, filters := range []string{"&category=" + url.QueryEscape("化学"), "&level=rare", "&view=saved", "&view=subscribed", "&country=" + url.QueryEscape("日本")} {
		if items := codeSearch(t, s, "q=PT01"+filters, reader); len(items) != 0 {
			t.Fatal("code ignored another explicit filter", filters)
		}
	}
	hidden := gap(t, s, "ordinary", "published", nil)
	if _, err := s.db.Exec("UPDATE gaps SET released_at=? WHERE id=?", stamp(s.now().Add(-72*time.Hour+time.Second)), hidden); err != nil {
		t.Fatal(err)
	}
	hiddenQuery := "q=" + gapCode(hidden, "ordinary", "物理")
	for _, cookie := range []string{"", reader} {
		if items := codeSearch(t, s, hiddenQuery, cookie); len(items) != 0 {
			t.Fatal("exact code exposed delayed content")
		}
	}
	if items := codeSearch(t, s, hiddenQuery, admin); len(items) != 1 || items[0].Body == "" {
		t.Fatal("administrator lost immediate full access")
	}
	pending := gap(t, s, "rare", "pending", authorID)
	pendingQuery := "q=" + gapCode(pending, "rare", "物理")
	if items := codeSearch(t, s, pendingQuery, reader); len(items) != 0 {
		t.Fatal("code exposed another member's pending submission")
	}
	if items := codeSearch(t, s, pendingQuery+"&view=mine", author); len(items) != 1 || items[0].Body == "" {
		t.Fatal("author could not search own submission")
	}
	if items := codeSearch(t, s, pendingQuery+"&status=all", admin); len(items) != 1 {
		t.Fatal("admin could not locate pending content")
	}
	base := s.now()
	s.now = func() time.Time { return base.Add(time.Second) }
	if items := codeSearch(t, s, hiddenQuery, reader); len(items) != 1 || items[0].Body != "" {
		t.Fatal("delay boundary or locked-body rule changed")
	}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", hidden), nil, reader), 200)
	if items := codeSearch(t, s, hiddenQuery, reader); len(items) != 1 || items[0].Body == "" {
		t.Fatal("code search lost legitimately unlocked text")
	}
}

func TestGapCodeSurvivesReviewLevelChange(t *testing.T) {
	s := testServer(t)
	_, admin := account(t, s, founderEmail, "admin", 0)
	id := gap(t, s, "ordinary", "pending", nil)
	for _, level := range []string{"ordinary", "featured", "rare", "exclusive"} {
		if _, err := s.db.Exec("UPDATE gaps SET status='pending' WHERE id=?", id); err != nil {
			t.Fatal(err)
		}
		mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/review/%d", id), map[string]string{"action": "approve", "level": level, "note": "已核验公开材料和具体结论适用范围"}, admin), 200)
		items := codeSearch(t, s, "q=PT01", admin)
		if len(items) != 1 || items[0].ID != id || items[0].Code != gapCode(id, level, "物理") || items[0].UnlockCost != unlockCosts[level] {
			t.Fatal("old code changed identity or ignored current classification", items)
		}
	}
	// An approved published revision freezes the previous code even when
	// reclassified; current fields still determine filtering and reading cost.
	item := editingItem(t, s, id, admin)
	oldCode := item.Gap.Code
	edit := editInput(item, "调整为旅行主题后的实用信息整理")
	edit.Category = "出行旅行"
	edit.Tags = []string{"国家:日本", "地区:东亚", "旅行:交通住宿"}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/submissions/%d/edit", id), edit, admin), 200)
	rev := editingItem(t, s, id, admin).Revision
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/revisions/%d/review", rev.ID), map[string]string{"action": "approve", "level": "ordinary", "edit_token": rev.EditToken}, admin), 200)
	for _, code := range []string{"PT01", "JX-WL-01", "XY-WL-01", "DJ-WL-01", "PT-LX-01", "pt-lx-01"} {
		items := codeSearch(t, s, "q="+code, admin)
		if len(items) != 1 || items[0].ID != id || items[0].Code != oldCode || items[0].Category != "出行旅行" || items[0].UnlockCost != 1 {
			t.Fatal("topic change broke a stable reference", code, items)
		}
	}
	if items := codeSearch(t, s, "q=DJ-WL-01&category="+url.QueryEscape("物理"), admin); len(items) != 0 {
		t.Fatal("old topic in a code bypassed the current explicit category filter")
	}
}

func TestGapCodeSearchStillRedactsInternalSource(t *testing.T) {
	s := testServer(t)
	_, reader := account(t, s, "reader@example.org", "member", 10)
	_, admin := account(t, s, founderEmail, "admin", 0)
	id := gap(t, s, "ordinary", "published", nil)
	if _, err := s.db.Exec("UPDATE gaps SET source_visibility='internal',source_name='LinuxDO',source_url='https://linux.do/t/topic/123',body='事实说明来自 LinuxDO 的原始线索 https://linux.do/t/topic/123',review_note='核验 LinuxDO 原帖' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", id), nil, reader), 200)
	items := codeSearch(t, s, "q=PT01", reader)
	if len(items) != 1 || items[0].Body == "" || items[0].SourceURL != "" || items[0].SourceName != "" || items[0].ReviewNote != "" || strings.Contains(strings.ToLower(items[0].Body), "linux") {
		t.Fatal("code lookup bypassed private-source redaction")
	}
	items = codeSearch(t, s, "q=PT01", admin)
	if len(items) != 1 || items[0].SourceURL == "" || !strings.Contains(items[0].Body, "LinuxDO") {
		t.Fatal("staff lost original evidence")
	}
}
