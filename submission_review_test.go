package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func submissionFixture(t *testing.T) (*server, *Gap, int64) {
	s := testServer(t)
	account(t, s, founderEmail, "admin", 0)
	author, _ := account(t, s, "submitter@example.test", "member", 10)
	id := gap(t, s, "ordinary", "pending", author)
	body := strings.Repeat("研究团队公布了公开结果。这项结果只适用于材料中列明的实验条件，普通读者可以先核对原始来源，再结合自己的使用场景判断是否适用。", 6)
	if _, e := s.db.Exec("UPDATE gaps SET title=?,summary=?,body=? WHERE id=?", "公开研究说明具体实验条件与使用范围", "研究的结果有明确适用条件，阅读时需要区分实验结论和延伸建议。", body, id); e != nil {
		t.Fatal(e)
	}
	if e := s.initSubmissionReview(); e != nil {
		t.Fatal(e)
	}
	g, e := gapScan(s.db.QueryRow(gapSelect+"WHERE g.id=?", 0, 0, id), &User{Role: "admin"})
	if e != nil {
		t.Fatal(e)
	}
	return s, g, author
}
func approvalRequest(g *Gap) submissionBridgeRequest {
	return submissionBridgeRequest{Action: "review", ID: g.ID, ExpectedUpdatedAt: g.UpdatedAt, ExpectedSubmissionHash: submissionHash(g), Decision: "approve", Level: "featured", Note: "已对照原始资料核对实验条件、具体结果与应用限制，正文没有把推测写成确定结论。", SourceHash: strings.Repeat("a", 64), Checks: []submissionCheck{{Claim: "实验条件已核对", Quote: "source provides a concrete experimental condition"}, {Claim: "结果适用范围已核对", Quote: "the observed results apply only to this experiment"}}}
}
func TestSubmissionReviewUsesExistingRewardTransaction(t *testing.T) {
	s, g, author := submissionFixture(t)
	req := approvalRequest(g)
	result, e := s.applySubmissionReview(req)
	if e != nil {
		t.Fatal(e)
	}
	if !result.(map[string]any)["applied"].(bool) {
		t.Fatal(result)
	}
	if _, e = s.applySubmissionReview(req); e == nil {
		t.Fatal("duplicate review should not apply")
	}
	var credits, points, grants, sessions int
	s.db.QueryRow("SELECT credits,points FROM users WHERE id=?", author).Scan(&credits, &points)
	s.db.QueryRow("SELECT COUNT(*) FROM contribution_rewards WHERE gap_id=?", g.ID).Scan(&grants)
	s.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE user_id=(SELECT id FROM users WHERE email=?)", founderEmail).Scan(&sessions)
	// Only account()'s original fixture session remains. The CLI token is gone.
	if credits != 210 || points != 80 || grants != 1 || sessions != 1 {
		t.Fatal(credits, points, grants, sessions)
	}
}
func TestSubmissionHoldMailRetryAndNoReward(t *testing.T) {
	s, g, author := submissionFixture(t)
	old := submissionNotificationMail
	t.Cleanup(func() { submissionNotificationMail = old })
	calls := 0
	failFirst := true
	submissionNotificationMail = func(_ context.Context, to, subject, body string) error {
		calls++
		if to != founderEmail || strings.Contains(body, g.SourceURL) || !strings.Contains(body, "#admin") {
			t.Fatal(to, body)
		}
		if failFirst {
			return errors.New("mail offline")
		}
		return nil
	}
	req := approvalRequest(g)
	req.Decision = "hold"
	req.Note = "来源链接需要进一步确认，已保留原稿供站长核对。"
	result, e := s.applySubmissionReview(req)
	if e != nil {
		t.Fatal(e)
	}
	if result.(map[string]any)["notification"] != "failed" {
		t.Fatal(result)
	}
	var status string
	var credits int
	s.db.QueryRow("SELECT status FROM gaps WHERE id=?", g.ID).Scan(&status)
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", author).Scan(&credits)
	if status != "pending" || credits != 10 {
		t.Fatal(status, credits)
	}
	queue, e := s.exportSubmissions()
	if e != nil {
		t.Fatal(e)
	}
	if queue.(map[string]any)["total"].(int) != 0 {
		t.Fatal(queue)
	}
	failFirst = false
	if e = s.retrySubmissionNotifications(); e != nil {
		t.Fatal(e)
	}
	if e = s.retrySubmissionNotifications(); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestSubmissionReviewOptimisticCheckAndNarrowInput(t *testing.T) {
	s, g, _ := submissionFixture(t)
	req := approvalRequest(g)
	s.db.Exec("UPDATE gaps SET summary='修改后的摘要必须重新核对，旧审核结论不能直接应用。' WHERE id=?", g.ID)
	if _, e := s.applySubmissionReview(req); e == nil {
		t.Fatal("changed content accepted")
	}
	for _, raw := range []string{`{"action":"sql","query":"DELETE FROM users"}`, `{"action":"export","email":"outside@example.com"}`, `{"action":"export"}{"action":"export"}`} {
		var out bytes.Buffer
		if e := runSubmissionReviewBridge(s, strings.NewReader(raw), &out); e == nil {
			t.Fatal("unexpected accepted", raw)
		}
	}
	var out bytes.Buffer
	if e := runSubmissionReviewBridge(s, strings.NewReader(`{"action":"export"}`), &out); e != nil {
		t.Fatal(e)
	}
	var result struct {
		Items []map[string]any `json:"items"`
	}
	if e := json.Unmarshal(out.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if len(result.Items) != 1 || result.Items[0]["body"] == "" {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "submitter@example.test") {
		t.Fatal("email leaked")
	}
}

func TestSubmissionDisabledAuthorExcludedAndOwnerHeld(t *testing.T) {
	s, g, author := submissionFixture(t)
	req := approvalRequest(g)
	s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", author)
	queue, e := s.exportSubmissions()
	if e != nil {
		t.Fatal(e)
	}
	if queue.(map[string]any)["total"].(int) != 0 {
		t.Fatal(queue)
	}
	if _, e = s.applySubmissionReview(req); e == nil {
		t.Fatal("disabled author approved")
	}
	var owner int64
	s.db.QueryRow("SELECT id FROM users WHERE email=?", founderEmail).Scan(&owner)
	s.db.Exec("UPDATE gaps SET author_id=? WHERE id=?", owner, g.ID)
	g, e = gapScan(s.db.QueryRow(gapSelect+"WHERE g.id=?", 0, 0, g.ID), &User{Role: "admin"})
	if e != nil {
		t.Fatal(e)
	}
	old := submissionNotificationMail
	t.Cleanup(func() { submissionNotificationMail = old })
	submissionNotificationMail = func(context.Context, string, string, string) error { return nil }
	result, e := s.applySubmissionReview(approvalRequest(g))
	if e != nil {
		t.Fatal(e)
	}
	if result.(map[string]any)["decision"] != "hold" {
		t.Fatal(result)
	}
	var status string
	s.db.QueryRow("SELECT status FROM gaps WHERE id=?", g.ID).Scan(&status)
	if status != "pending" {
		t.Fatal(status)
	}
}
