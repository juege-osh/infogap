package main

import (
	"fmt"
	"strconv"
	"strings"
)

// The AUTOINCREMENT primary key is the stable, site-wide sequence. Only the
// prefix changes when reviewers change an item's level; no second ID is stored.
func gapCode(id int64, level string) string {
	prefix := "PT"
	switch level {
	case "featured":
		prefix = "JX"
	case "rare":
		prefix = "XY"
	case "exclusive":
		prefix = "DJ"
	}
	return fmt.Sprintf("%s%02d", prefix, id)
}

func gapCodeID(code string) (int64, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) < 4 {
		return 0, false
	}
	switch code[:2] {
	case "PT", "JX", "XY", "DJ":
	default:
		return 0, false
	}
	for _, char := range code[2:] {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(code[2:], 10, 64)
	// Accept an old level prefix so a saved reference survives reclassification.
	return id, err == nil && id > 0
}
