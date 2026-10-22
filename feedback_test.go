package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func feedbackFixture(t *testing.T) (*server, int64, string, string, string) {
	s := testServer(t)
	if err := initFeedback(s.db); err != nil {
		t.Fatal(err)
	}
	id, cookie := account(t, s, "feedbackowner@example.test", "member", 10)
	_, other := account(t, s, "otherfeedback@example.test", "reviewer", 0)
	_, admin := account(t, s, founderEmail, "admin", 0)
	return s, id, cookie, other, admin
}
func createFeedbackFixture(t *testing.T, s *server, cookie string) int64 {
	t.Helper()
	w := request(s, "POST", "/api/feedback", map[string]string{"title": "播放建议", "body": "希望播放器可以记住上一次设置的音量。", "type": "idea"}, cookie)
	mustStatus(t, w, 201)
	var result struct {
		Feedback feedbackItem `json:"feedback"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Feedback.ID
}
func TestFeedbackPrivacyRepliesAndStatus(t *testing.T) {
	s, _, owner, other, admin := feedbackFixture(t)
	id := createFeedbackFixture(t, s, owner)
	path := fmt.Sprintf("/api/feedback/%d", id)
	mustStatus(t, request(s, "GET", "/api/feedback", nil, ""), 401)
	mustStatus(t, request(s, "GET", path, nil, other), 404)
	mustStatus(t, request(s, "POST", path+"/replies", map[string]string{"body": "不能查看别人的意见也不能回复"}, other), 404)
	for _, cookie := range []string{owner, admin} {
		mustStatus(t, request(s, "POST", path+"/replies", map[string]string{"body": "这条回复是纯文本 <script>alert('test')</script>。"}, cookie), 201)
	}
	w := request(s, "GET", path, nil, owner)
	mustStatus(t, w, 200)
	var detail struct {
		Feedback feedbackItem    `json:"feedback"`
		Replies  []feedbackReply `json:"replies"`
	}
	json.Unmarshal(w.Body.Bytes(), &detail)
	if len(detail.Replies) != 2 || detail.Replies[0].IsAdmin || !detail.Replies[1].IsAdmin || detail.Feedback.ReplyCount != 2 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "@example.test") || strings.Contains(w.Body.String(), founderEmail) {
		t.Fatal("private email leaked")
	}
	statusPath := fmt.Sprintf("/api/admin/feedback/%d", id)
	mustStatus(t, request(s, "POST", statusPath, map[string]string{"status": "working"}, owner), 403)
	mustStatus(t, request(s, "POST", statusPath, map[string]string{"status": "working"}, other), 403)
	for _, status := range []string{"reviewing", "planned", "working", "resolved", "declined", "new"} {
		mustStatus(t, request(s, "POST", statusPath, map[string]string{"status": status}, admin), 200)
	}
	mustStatus(t, request(s, "POST", statusPath, map[string]string{"status": "invalid"}, admin), 400)
	w = request(s, "GET", "/api/feedback", nil, other)
	mustStatus(t, w, 200)
	var listing struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &listing)
	if listing.Total != 0 {
		t.Fatal(w.Body.String())
	}
	w = request(s, "GET", "/api/feedback?status=new", nil, admin)
	json.Unmarshal(w.Body.Bytes(), &listing)
	if listing.Total != 1 {
		t.Fatal(w.Body.String())
	}
}
func TestFeedbackRewardConcurrentExactlyOnceAndPointsUnchanged(t *testing.T) {
	s, user, owner, other, admin := feedbackFixture(t)
	id := createFeedbackFixture(t, s, owner)
	path := fmt.Sprintf("/api/admin/feedback/%d/reward", id)
	for _, cookie := range []string{owner, other} {
		mustStatus(t, request(s, "POST", path, map[string]int{"credits": 77}, cookie), 403)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- request(s, "POST", path, map[string]int{"credits": 77}, admin).Code }()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatal(code)
		}
	}
	var credits, points, count, ledger, awarded int
	s.db.QueryRow("SELECT credits,points FROM users WHERE id=?", user).Scan(&credits, &points)
	s.db.QueryRow("SELECT COUNT(*) FROM feedback_rewards WHERE feedback_id=?", id).Scan(&count)
	s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE user_id=? AND kind='feedback_reward'", user).Scan(&ledger)
	s.db.QueryRow("SELECT reward_credits FROM feedback WHERE id=?", id).Scan(&awarded)
	if credits != 87 || points != 0 || count != 1 || ledger != 1 || awarded != 77 {
		t.Fatal(credits, points, count, ledger, awarded)
	}
	mustStatus(t, request(s, "POST", path, map[string]int{"credits": 78}, admin), 409)
	for _, value := range []any{0, -1, 10001, 1.5, "100"} {
		mustStatus(t, request(s, "POST", path, map[string]any{"credits": value}, admin), 400)
	}
}
func TestFeedbackRewardRollbackAndValidation(t *testing.T) {
	s, user, owner, _, admin := feedbackFixture(t)
	id := createFeedbackFixture(t, s, owner)
	if _, err := s.db.Exec(`CREATE TRIGGER feedback_reward_abort BEFORE INSERT ON feedback_rewards BEGIN SELECT RAISE(ABORT,'test reward rollback'); END`); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/feedback/%d/reward", id), map[string]int{"credits": 100}, admin), 409)
	var credits, count int
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", user).Scan(&credits)
	s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE kind='feedback_reward'").Scan(&count)
	if credits != 10 || count != 0 {
		t.Fatal(credits, count)
	}
	for _, in := range []map[string]string{{"title": "短", "body": "足够长的意见内容用来测试标题门槛。", "type": "idea"}, {"title": "标题正常", "body": "太短", "type": "problem"}, {"title": "标题正常", "body": "足够长的意见内容用来测试类型校验。", "type": "spam"}} {
		mustStatus(t, request(s, "POST", "/api/feedback", in, owner), 400)
	}
	mustStatus(t, request(s, "GET", "/api/feedback?status=all", nil, owner), 400)
	mustStatus(t, request(s, "GET", "/api/feedback?page=bad", nil, owner), 400)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/feedback/%d/replies", id), map[string]string{"body": " "}, owner), 400)
}
func TestFeedbackPaginationScopeAndDailyLimit(t *testing.T) {
	s, user, owner, _, admin := feedbackFixture(t)
	for i := 0; i < 23; i++ {
		if _, err := s.db.Exec("INSERT INTO feedback(user_id,title,body,type,created_at,updated_at) VALUES(?,?,?,'idea',?,?)", user, fmt.Sprintf("反馈标题%d", i), "测试分页所需的意见内容，只有本人及站长可见。", stamp(s.now()), stamp(s.now())); err != nil {
			t.Fatal(err)
		}
	}
	w := request(s, "GET", "/api/feedback?page=2", nil, owner)
	mustStatus(t, w, 200)
	var result struct {
		Items              []feedbackItem `json:"items"`
		Page, Pages, Total int
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if len(result.Items) != 3 || result.Page != 2 || result.Pages != 2 || result.Total != 23 {
		t.Fatal(w.Body.String())
	}
	mustStatus(t, request(s, "POST", "/api/feedback", map[string]string{"title": "新的反馈", "body": "这条意见应因为每日提交次数达到上限而被阻止。", "type": "problem"}, owner), 429)
	w = request(s, "GET", "/api/feedback?page=2", nil, admin)
	mustStatus(t, w, 200)
}
