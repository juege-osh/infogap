package main

import "testing"

func TestChineseReadingReady(t *testing.T) {
	body := "这项研究比较了不同实验条件下的变化，结果还需要进一步验证。"
	if chineseReadingReady("Global emissions slow but warming continues", "这是一则中文摘要。", body) {
		t.Fatal("English title published")
	}
	if !chineseReadingReady("Global emissions slow（全球排放增速放缓）", "这是一则中文摘要。", body) {
		t.Fatal("bilingual title rejected")
	}
	if chineseReadingReady("中文标题", "这是一则中文摘要。", "This study compares climate change and global emissions.") {
		t.Fatal("English body published")
	}
}
