package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func travelGap(t *testing.T, s *server, country, region, topic, status string) int64 {
	t.Helper()
	id := gap(t, s, "ordinary", status, nil)
	tags, err := json.Marshal([]string{"国家:" + country, "地区:" + region, "旅行:" + topic})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE gaps SET category='出行旅行',tags=? WHERE id=?", string(tags), id); err != nil {
		t.Fatal(err)
	}
	return id
}

func travelCountries(t *testing.T, s *server, cookie string) []string {
	t.Helper()
	w := request(s, "GET", "/api/travel/facets", nil, cookie)
	mustStatus(t, w, 200)
	var result struct{ Countries []string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Countries
}

func TestTravelFiltersRequireExactCountryRegionAndTopic(t *testing.T) {
	s := testServer(t)
	target := travelGap(t, s, "日本", "东亚", "优惠渠道", "published")
	travelGap(t, s, "日本", "东亚", "安全提醒", "published")
	travelGap(t, s, "日本", "欧洲", "优惠渠道", "published")
	travelGap(t, s, "日本周边", "东亚", "优惠渠道", "published")
	other := travelGap(t, s, "日本", "东亚", "优惠渠道", "published")
	if _, err := s.db.Exec("UPDATE gaps SET category='省钱优惠' WHERE id=?", other); err != nil {
		t.Fatal(err)
	}
	path := "/api/gaps?" + url.Values{"country": {"日本"}, "region": {"东亚"}, "travel_topic": {"优惠渠道"}}.Encode()
	w := request(s, "GET", path, nil, "")
	mustStatus(t, w, 200)
	var result struct {
		Items []Gap `json:"items"`
		Total int   `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].ID != target || result.Items[0].Body != "" {
		t.Fatalf("travel filters must AND exact tags and preserve locks: %s", w.Body.String())
	}
	for _, country := range []string{"日", "日本%", "' OR 1=1 --", "未收录国家"} {
		w = request(s, "GET", "/api/gaps?country="+url.QueryEscape(country), nil, "")
		mustStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), `"total":0`) {
			t.Fatalf("country was not matched exactly: %s", w.Body.String())
		}
	}
	for _, values := range []url.Values{{"country": {strings.Repeat("字", 22)}}, {"country": {"日\x00本"}}, {"country": {"<日本>"}}, {"region": {"不存在地区"}}, {"travel_topic": {"不存在主题"}}} {
		mustStatus(t, request(s, "GET", "/api/gaps?"+values.Encode(), nil, ""), 400)
	}
}

func TestTravelFacetsAndFiltersPreservePublicationDelay(t *testing.T) {
	s := testServer(t)
	readerID, reader := account(t, s, "reader@example.org", "member", 10)
	_, admin := account(t, s, founderEmail, "admin", 0)
	travelGap(t, s, "日本", "东亚", "优惠渠道", "published")
	hidden := travelGap(t, s, "法国", "欧洲", "景点避坑", "published")
	travelGap(t, s, "尼泊尔", "南亚", "交通住宿", "pending")
	travelGap(t, s, "智利", "南美洲", "安全提醒", "disputed")
	if _, err := s.db.Exec("UPDATE gaps SET released_at=? WHERE id=?", stamp(s.now().Add(time.Second)), hidden); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE users SET membership_until=? WHERE id=?", stamp(s.now().Add(time.Hour)), readerID); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []string{"", reader} {
		if got := travelCountries(t, s, cookie); !slices.Equal(got, []string{"日本"}) {
			t.Fatalf("facet revealed a delayed or unpublished item: %v", got)
		}
		w := request(s, "GET", "/api/gaps?country="+url.QueryEscape("法国"), nil, cookie)
		mustStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), `"total":0`) || strings.Contains(w.Body.String(), "PROTECTED_SECRET") {
			t.Fatal("travel filter bypassed delay", w.Body.String())
		}
	}
	if got := travelCountries(t, s, admin); !slices.Equal(got, []string{"日本", "法国"}) {
		t.Fatalf("staff lost immediate published access or gained pending facets: %v", got)
	}
	base := s.now()
	s.now = func() time.Time { return base.Add(time.Second) }
	if got := travelCountries(t, s, reader); !slices.Equal(got, []string{"日本", "法国"}) {
		t.Fatalf("facet did not open at the exact delay boundary: %v", got)
	}
	w := request(s, "GET", "/api/gaps?country="+url.QueryEscape("法国"), nil, reader)
	mustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"total":1`) || strings.Contains(w.Body.String(), "PROTECTED_SECRET") {
		t.Fatal("visible travel metadata leaked locked body", w.Body.String())
	}
}

func TestTravelFacetsRetainPriorUnlockAndHidePrivateSourceTags(t *testing.T) {
	s := testServer(t)
	readerID, reader := account(t, s, "reader@example.org", "member", 10)
	_, admin := account(t, s, founderEmail, "admin", 0)
	fresh := travelGap(t, s, "日本", "东亚", "支付通信", "published")
	if _, err := s.db.Exec("UPDATE gaps SET released_at=? WHERE id=?", stamp(s.now()), fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE users SET points=6500 WHERE id=?", readerID); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", fresh), nil, reader), 200)
	if _, err := s.db.Exec("UPDATE users SET points=0 WHERE id=?", readerID); err != nil {
		t.Fatal(err)
	}
	if got := travelCountries(t, s, reader); !slices.Equal(got, []string{"日本"}) {
		t.Fatal("prior unlock was hidden after rank change", got)
	}
	if got := travelCountries(t, s, ""); !slices.Equal(got, []string{"日本"}) {
		t.Fatal("guest inferred reader's unlock", got)
	}
	private := travelGap(t, s, "LinuxDO", "东亚", "优惠渠道", "published")
	if _, err := s.db.Exec("UPDATE gaps SET source_visibility='internal',source_name='私密科技社',source_url='https://linux.do/t/123',review_note='private-note' WHERE id=?", private); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/gaps/%d/unlock", private), nil, reader), 200)
	for _, cookie := range []string{"", reader} {
		w := request(s, "GET", "/api/travel/facets", nil, cookie)
		mustStatus(t, w, 200)
		assertNoPrivateSource(t, w.Body.String())
		w = request(s, "GET", "/api/gaps?country=LinuxDO", nil, cookie)
		mustStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), `"total":0`) {
			t.Fatal("country filter inferred private provenance", w.Body.String())
		}
	}
	if !slices.Contains(travelCountries(t, s, admin), "LinuxDO") {
		t.Fatal("staff lost original country tag")
	}
}

func TestBootstrapTravelEnumsAreSeparateFromCategories(t *testing.T) {
	s := testServer(t)
	w := request(s, "GET", "/api/bootstrap", nil, "")
	mustStatus(t, w, 200)
	var result struct {
		Categories []string `json:"categories"`
		Topics     []string `json:"travel_topics"`
		Regions    []string `json:"travel_regions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Categories) != 28 || !slices.Equal(result.Topics, []string{"签证入境", "景点避坑", "交通住宿", "优惠渠道", "支付通信", "季节天气", "安全提醒"}) || !slices.Equal(result.Regions, []string{"东亚", "东南亚", "南亚", "中亚", "西亚", "欧洲", "非洲", "北美洲", "南美洲", "大洋洲", "南极洲", "跨地区"}) {
		t.Fatalf("travel catalog mismatch: %s", w.Body.String())
	}
}
