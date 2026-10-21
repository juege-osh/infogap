package main

import (
	"fmt"
	"strconv"
	"strings"
)

var topicCodes = map[string]string{
	"前沿科技": "KJ", "人工智能": "AI", "生命科学": "SM", "物理": "WL",
	"化学": "HX", "地球与地理": "DL", "天文与空间": "TW", "材料科学": "CL",
	"开源技术": "KY", "军事与国防": "JS", "政治与公共政策": "ZZ", "农业与食品": "NY",
	"社会民生": "MS", "法律法规": "FL", "航空航天": "HT", "经济与产业": "JJ",
	"能源与环境": "NH", "医疗健康": "YL", "教育与就业": "JY", "交通与基建": "JT",
	"公共安全": "AQ", "生活实用": "SH", "省钱优惠": "SQ", "招聘求职": "ZP",
	"职业成长": "ZY", "住房居住": "ZF", "出行旅行": "LX", "商业创业": "CY",
}

var gapLevelPrefixes = map[string]string{"ordinary": "PT", "featured": "JX", "rare": "XY", "exclusive": "DJ"}

// The AUTOINCREMENT primary key is the stable, site-wide sequence. Classification
// changes only replace the level/topic prefixes; no second ID is stored.
func gapCode(id int64, level, category string) string {
	prefix, topic := gapLevelPrefixes[level], topicCodes[category]
	if prefix == "" || topic == "" || id <= 0 {
		return ""
	}
	return fmt.Sprintf("%s-%s-%02d", prefix, topic, id)
}

func gapCodeID(code string) (int64, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) < 4 {
		return 0, false
	}
	prefix, digits := code[:2], code[2:]
	if strings.Contains(code, "-") {
		parts := strings.Split(code, "-")
		if len(parts) != 3 {
			return 0, false
		}
		prefix, digits = parts[0], parts[2]
		knownTopic := false
		for _, topic := range topicCodes {
			if parts[1] == topic {
				knownTopic = true
				break
			}
		}
		if !knownTopic {
			return 0, false
		}
	}
	switch prefix {
	case "PT", "JX", "XY", "DJ":
	default:
		return 0, false
	}
	if len(digits) < 2 {
		return 0, false
	}
	for _, char := range digits {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(digits, 10, 64)
	// Accept the original PT01 format and old level/topic prefixes so references
	// survive reclassification. The response always shows the current code.
	return id, err == nil && id > 0
}
