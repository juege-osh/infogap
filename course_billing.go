package main

import (
	"strconv"
	"strings"
)

func parseCoursePlanID(plan string) (int64, bool) {
	if !strings.HasPrefix(plan, "course_") {
		return 0, false
	}
	raw := strings.TrimPrefix(plan, "course_")
	if raw == "" {
		return 0, false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0 && strconv.FormatInt(id, 10) == raw
}
