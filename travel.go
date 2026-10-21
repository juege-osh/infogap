package main

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var travelTopics = []string{"签证入境", "景点避坑", "交通住宿", "优惠渠道", "支付通信", "季节天气", "安全提醒"}
var travelRegions = []string{"东亚", "东南亚", "南亚", "中亚", "西亚", "欧洲", "非洲", "北美洲", "南美洲", "大洋洲", "南极洲", "跨地区"}

func validTravelCountry(value string) bool {
	if value != strings.TrimSpace(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 21 || strings.ContainsAny(value, "<>") {
		return false
	}
	for _, ch := range value {
		if unicode.IsControl(ch) {
			return false
		}
	}
	return true
}

func travelFilter(query url.Values, u *User) (string, []any, error) {
	where := ""
	args := []any{}
	for _, field := range []struct{ key, prefix string }{{"country", "国家:"}, {"region", "地区:"}, {"travel_topic", "旅行:"}} {
		value := strings.TrimSpace(query.Get(field.key))
		if value == "" {
			continue
		}
		if (field.key == "country" && !validTravelCountry(value)) || (field.key == "region" && !has(travelRegions, value)) || (field.key == "travel_topic" && !has(travelTopics, value)) {
			return "", nil, errors.New("请选择有效的地区与旅行标签，国家名称需 1–21 字")
		}
		where += " AND EXISTS(SELECT 1 FROM json_each(CASE WHEN json_valid(g.tags) THEN g.tags ELSE '[]' END) travel_tag WHERE " + publicTextSQL("travel_tag.value", u) + "=?)"
		args = append(args, field.prefix+value)
	}
	if len(args) > 0 {
		where = " AND g.category='出行旅行'" + where
	}
	return where, args, nil
}

func (s *server) travelFacets(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	visible, args := s.visibilityClause(u)
	publicTag := publicTextSQL("travel_tag.value", u)
	// Do not turn a private source identifier into a selectable country. The same
	// redaction used by search also guards facets, including previously unlocked items.
	query := "SELECT DISTINCT " + publicTag + " FROM gaps g,json_each(CASE WHEN json_valid(g.tags) THEN g.tags ELSE '[]' END) travel_tag WHERE g.category='出行旅行' AND g.status='published' AND travel_tag.type='text' AND " + publicTag + "=travel_tag.value" + visible
	rows, err := s.db.Query(query, args...)
	if err != nil {
		fail(w, 500, "读取旅行国家失败")
		return
	}
	defer rows.Close()
	countries := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			fail(w, 500, "读取旅行国家失败")
			return
		}
		if !strings.HasPrefix(tag, "国家:") {
			continue
		}
		country := strings.TrimPrefix(tag, "国家:")
		if validTravelCountry(country) && !seen[country] {
			seen[country] = true
			countries = append(countries, country)
		}
	}
	if err := rows.Err(); err != nil {
		fail(w, 500, "读取旅行国家失败")
		return
	}
	sort.Strings(countries)
	send(w, 200, map[string]any{"countries": countries})
}
