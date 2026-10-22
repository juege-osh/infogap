package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func editingItem(t *testing.T, s *server, id int64, cookie string) *submissionItem {
	t.Helper()
	w := request(s, "GET", fmt.Sprintf("/api/submissions/%d/edit", id), nil, cookie)
	mustStatus(t, w, 200)
	var v submissionItem
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return &v
}
func editInput(item *submissionItem, title string) submissionEditInput {
	return submissionEditInput{Title: title, Summary: "这项研究总结了新的实验结果，适用条件与证据限制需要根据原文核对。", Body: strings.Repeat("研究团队公开了实验方法和适用边界，实际结果仍需在相同条件下独立复核。", 12), Category: "物理", Level: "exclusive", Evidence: "official", Tags: []string{"实验"}, EditToken: item.EditToken}
}
func TestSubmissionOwnerReadFilterResubmitAndFinalReviewReward(t *testing.T) {
	s := testServer(t)
	ownerID, owner := account(t, s, "edit-author@example.org", "member", 0)
	_, other := account(t, s, "edit-other@example.org", "member", 10)
	_, admin := account(t, s, founderEmail, "admin", 0)
	id := gap(t, s, "exclusive", "rejected", ownerID)
	otherID := gap(t, s, "ordinary", "pending", nil)
	path := fmt.Sprintf("/api/submissions/%d/edit", id)
	mustStatus(t, request(s, "GET", path, nil, ""), 401)
	mustStatus(t, request(s, "GET", path, nil, other), 404)
	item := editingItem(t, s, id, owner)
	if !item.Editable || item.Gap.Body == "" || item.Gap.SourceURL == "" || !item.Gap.Unlocked {
		t.Fatal("author cannot read original", item)
	}
	var balance, unlocks int
	s.db.QueryRow("SELECT credits FROM users WHERE id=?", ownerID).Scan(&balance)
	s.db.QueryRow("SELECT COUNT(*) FROM unlocks WHERE user_id=?", ownerID).Scan(&unlocks)
	if balance != 0 || unlocks != 0 {
		t.Fatal("owner reading charged")
	}
	input := editInput(item, "重新整理的研究方法与证据限制")
	mustStatus(t, request(s, "POST", path, input, other), 404)
	mustStatus(t, request(s, "POST", path, input, owner), 200)
	mustStatus(t, request(s, "POST", path, input, owner), 409)
	fresh := editingItem(t, s, id, owner)
	if fresh.Gap.Status != "pending" || fresh.EditToken == item.EditToken || fresh.Revision != nil {
		t.Fatal("pending retry state invalid")
	}
	// Repeating identical content in the same clock second still changes the
	// optimistic token and automatic review snapshot through edit_version.
	before, _ := s.oneGap(id, &User{Role: "admin"})
	input.EditToken = fresh.EditToken
	mustStatus(t, request(s, "POST", path, input, owner), 200)
	after, _ := s.oneGap(id, &User{Role: "admin"})
	if submissionHash(before) == submissionHash(after) {
		t.Fatal("same-second resubmission reused review snapshot")
	}
	w := request(s, "GET", "/api/me/submissions?status=pending&q=重新整理&category=物理", nil, owner)
	mustStatus(t, w, 200)
	var list struct {
		Items []submissionItem
		Total int
	}
	json.Unmarshal(w.Body.Bytes(), &list)
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].Gap.ID != id {
		t.Fatal(w.Body.String())
	}
	w = request(s, "GET", "/api/me/submissions?status=rejected", nil, owner)
	mustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatal(w.Body.String())
	}
	mustStatus(t, request(s, "GET", fmt.Sprintf("/api/submissions/%d/edit", otherID), nil, owner), 404)
	// Author's proposed exclusive level does not grant exclusive-level rewards.
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/review/%d", id), map[string]string{"action": "approve", "level": "ordinary", "note": "已按最终证据分级"}, admin), 200)
	var points, rewards int
	s.db.QueryRow("SELECT credits,points FROM users WHERE id=?", ownerID).Scan(&balance, &points)
	s.db.QueryRow("SELECT COUNT(*) FROM contribution_rewards WHERE gap_id=?", id).Scan(&rewards)
	if balance != 100 || points != 20 || rewards != 1 {
		t.Fatal("reward followed self-rating", balance, points, rewards)
	}
}
func TestPublishedRevisionKeepsOriginalUnlocksAndRewards(t *testing.T) {
	s := testServer(t)
	ownerID, owner := account(t, s, "revision-author@example.org", "member", 0)
	readerID, reader := account(t, s, "revision-reader@example.org", "member", 10)
	_, admin := account(t, s, founderEmail, "admin", 0)
	id := gap(t, s, "ordinary", "pending", ownerID)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/review/%d", id), map[string]string{"action": "approve", "level": "ordinary"}, admin), 200)
	// Old enough to be visible; release time must survive revision approval.
	_, err := s.db.Exec("UPDATE gaps SET released_at=published_at WHERE id=?", id)
	if err != nil {
		t.Fatal(err)
	}
	// Published source dates of pending fixtures are current; bypass via a
	// direct unlock fixture to test durable previously granted reading rights.
	_, err = s.db.Exec("INSERT INTO unlocks(user_id,gap_id,cost,kind,created_at) VALUES(?,?,0,'fixture',?)", readerID, id, stamp(s.now()))
	if err != nil {
		t.Fatal(err)
	}
	original, _ := s.oneGap(id, &User{Role: "admin"})
	item := editingItem(t, s, id, owner)
	input := editInput(item, "修订后的研究结论与明确适用边界")
	path := fmt.Sprintf("/api/submissions/%d/edit", id)
	mustStatus(t, request(s, "POST", path, input, owner), 200)
	current, _ := s.oneGap(id, &User{Role: "admin"})
	if current.Body != original.Body || current.Code != original.Code || current.Status != "published" || current.ReleasedAt != original.ReleasedAt {
		t.Fatal("published original replaced before review")
	}
	item = editingItem(t, s, id, owner)
	if item.Revision == nil || item.Revision.Draft.Title != input.Title || item.Gap.Title != original.Title {
		t.Fatal("missing separate revision")
	}
	w := request(s, "GET", fmt.Sprintf("/api/gaps/%d", id), nil, reader)
	mustStatus(t, w, 200)
	if strings.Contains(w.Body.String(), input.Title) || !strings.Contains(w.Body.String(), "PROTECTED_SECRET_FULLTEXT") {
		t.Fatal("draft leaked or old unlocked copy hidden", w.Body.String())
	}
	rev := item.Revision
	reviewPath := fmt.Sprintf("/api/admin/revisions/%d/review", rev.ID)
	mustStatus(t, request(s, "POST", reviewPath, map[string]string{"action": "approve", "level": "ordinary", "edit_token": rev.EditToken}, owner), 403)
	mustStatus(t, request(s, "POST", reviewPath, map[string]string{"action": "reject", "note": "请补充实验条件", "edit_token": rev.EditToken}, admin), 200)
	current, _ = s.oneGap(id, &User{Role: "admin"})
	if current.Body != original.Body {
		t.Fatal("reject changed original")
	}
	item = editingItem(t, s, id, owner)
	input.EditToken = item.EditToken
	input.Title = "第二次修订的研究结论与适用边界"
	mustStatus(t, request(s, "POST", path, input, owner), 200)
	item = editingItem(t, s, id, owner)
	rev = item.Revision
	mustStatus(t, request(s, "POST", reviewPath, map[string]string{"action": "approve", "level": "ordinary", "edit_token": item.EditToken}, admin), 409)
	reviewPath = fmt.Sprintf("/api/admin/revisions/%d/review", rev.ID)
	approve := map[string]string{"action": "approve", "level": "featured", "note": "已核验修订所附实验方法和限制", "edit_token": rev.EditToken}
	mustStatus(t, request(s, "POST", reviewPath, approve, admin), 200)
	mustStatus(t, request(s, "POST", reviewPath, approve, admin), 409)
	current, _ = s.oneGap(id, &User{Role: "admin"})
	if current.ID != original.ID || current.ReleasedAt != original.ReleasedAt || current.Code != original.Code || current.Level != "featured" || current.Title != input.Title || current.Status != "published" {
		t.Fatal("revision publish incorrect", current)
	}
	var credits, points, rewards, unlocks int
	s.db.QueryRow("SELECT credits,points FROM users WHERE id=?", ownerID).Scan(&credits, &points)
	s.db.QueryRow("SELECT COUNT(*) FROM contribution_rewards WHERE gap_id=?", id).Scan(&rewards)
	s.db.QueryRow("SELECT COUNT(*) FROM unlocks WHERE user_id=? AND gap_id=?", readerID, id).Scan(&unlocks)
	if credits != 100 || points != 20 || rewards != 1 || unlocks != 1 {
		t.Fatal("revision repaid contribution or lost access", credits, points, rewards, unlocks)
	}
	w = request(s, "GET", fmt.Sprintf("/api/gaps/%d", id), nil, reader)
	mustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), input.Title) || !strings.Contains(w.Body.String(), input.Body) {
		t.Fatal("previous reader cannot read replacement")
	}
}
func TestSubmissionEditsOptimisticConcurrencyPrivacyAndStaleApproval(t *testing.T) {
	s := testServer(t)
	ownerID, owner := account(t, s, "privacy-author@example.org", "member", 0)
	_, admin := account(t, s, founderEmail, "admin", 0)
	id := gap(t, s, "ordinary", "published", ownerID)
	_, err := s.db.Exec("UPDATE gaps SET source_visibility='internal',source_name='LinuxDO',source_url='https://linux.do/t/revision-secret',body='LinuxDO 私人讨论 https://linux.do/t/revision-secret',review_note='LinuxDO 内部评审' WHERE id=?", id)
	if err != nil {
		t.Fatal(err)
	}
	item := editingItem(t, s, id, owner)
	raw, _ := json.Marshal(item)
	assertNoPrivateSource(t, string(raw))
	input := editInput(item, "修订后的研究事实和已知限制")
	path := fmt.Sprintf("/api/submissions/%d/edit", id)
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); statuses <- request(s, "POST", path, input, owner).Code }()
	}
	wg.Wait()
	close(statuses)
	ok, conflict := 0, 0
	for code := range statuses {
		if code == 200 {
			ok++
		}
		if code == 409 {
			conflict++
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal("optimistic concurrent edits not isolated", ok, conflict)
	}
	item = editingItem(t, s, id, owner)
	raw, _ = json.Marshal(item)
	assertNoPrivateSource(t, string(raw))
	if item.Revision.Draft.Evidence != "unverified" {
		t.Fatal("private revision asserted verified evidence")
	}
	// A separate administrative action invalidates the base snapshot. It cannot
	// be silently overwritten by an already prepared revision approval.
	_, err = s.db.Exec("UPDATE gaps SET title='管理人员已修改公开稿件标题',edit_version=edit_version+1 WHERE id=?", id)
	if err != nil {
		t.Fatal(err)
	}
	rv := item.Revision
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/revisions/%d/review", rv.ID), map[string]string{"action": "approve", "level": "ordinary", "edit_token": rv.EditToken}, admin), 409)
	for _, status := range []string{"disputed", "expired"} {
		_, _ = s.db.Exec("UPDATE gaps SET status=? WHERE id=?", status, id)
		item = editingItem(t, s, id, owner)
		if item.Editable {
			t.Fatal("moderated original became editable")
		}
		input.EditToken = item.EditToken
		mustStatus(t, request(s, "POST", path, input, owner), 409)
	}
}

func TestRevisionPublicationLengthBoundariesAndSelfReview(t *testing.T) {
	for _, length := range []int{299, 300, 1000, 1001} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			s := testServer(t)
			ownerID, owner := account(t, s, "reviewer-author@example.org", "reviewer", 0)
			_, reviewer := account(t, s, "other-reviewer@example.org", "reviewer", 0)
			_, admin := account(t, s, founderEmail, "admin", 0)
			id := gap(t, s, "ordinary", "published", ownerID)
			path := fmt.Sprintf("/api/submissions/%d/edit", id)
			mustStatus(t, request(s, "GET", path, nil, reviewer), 404)
			item := editingItem(t, s, id, owner)
			input := editInput(item, "修订后的研究事实与适用范围说明")
			input.Body = strings.Repeat("文", length)
			mustStatus(t, request(s, "POST", path, input, owner), 200)
			rev := editingItem(t, s, id, admin).Revision
			reviewPath := fmt.Sprintf("/api/admin/revisions/%d/review", rev.ID)
			approval := map[string]string{"action": "approve", "level": "ordinary", "edit_token": rev.EditToken}
			mustStatus(t, request(s, "POST", reviewPath, approval, owner), 403)
			want := 200
			if length < 300 || length > 1000 {
				want = 400
			}
			mustStatus(t, request(s, "POST", reviewPath, approval, reviewer), want)
			current, err := s.oneGap(id, &User{Role: "admin"})
			if err != nil {
				t.Fatal(err)
			}
			if (current.Body == input.Body) != (want == 200) {
				t.Fatal("publication boundary changed original incorrectly")
			}
		})
	}
}

func TestRevisionEditAndApprovalAuditFailureRollBack(t *testing.T) {
	s := testServer(t)
	ownerID, owner := account(t, s, "rollback-author@example.org", "member", 0)
	_, admin := account(t, s, founderEmail, "admin", 0)
	id := gap(t, s, "ordinary", "published", ownerID)
	path := fmt.Sprintf("/api/submissions/%d/edit", id)
	item := editingItem(t, s, id, owner)
	input := editInput(item, "修订后的研究事实与已知方法限制")
	if _, err := s.db.Exec("CREATE TRIGGER fail_revision_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT,'audit failure'); END"); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "POST", path, input, owner), 500)
	unchanged := editingItem(t, s, id, owner)
	if unchanged.EditToken != item.EditToken || unchanged.Revision != nil {
		t.Fatal("failed edit committed draft or version")
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_revision_audit"); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "POST", path, input, owner), 200)
	rev := editingItem(t, s, id, owner).Revision
	if _, err := s.db.Exec("CREATE TRIGGER fail_revision_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT,'audit failure'); END"); err != nil {
		t.Fatal(err)
	}
	replacement := editInput(editingItem(t, s, id, owner), "再次修改的研究事实和适用范围说明")
	mustStatus(t, request(s, "POST", path, replacement, owner), 500)
	if kept := editingItem(t, s, id, owner).Revision; kept.ID != rev.ID || kept.EditToken != rev.EditToken {
		t.Fatal("failed replacement superseded the existing active revision")
	}
	approval := map[string]string{"action": "approve", "level": "featured", "edit_token": rev.EditToken}
	reviewPath := fmt.Sprintf("/api/admin/revisions/%d/review", rev.ID)
	mustStatus(t, request(s, "POST", reviewPath, approval, admin), 500)
	unchanged = editingItem(t, s, id, owner)
	if unchanged.Gap.Body != item.Gap.Body || unchanged.Gap.Code != item.Gap.Code || unchanged.Revision.Status != "pending" || unchanged.Revision.EditToken != rev.EditToken {
		t.Fatal("failed approval partially replaced original")
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_revision_audit"); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, request(s, "POST", reviewPath, approval, admin), 200)
	// A second approved revision changes the theme but retains the first public code.
	item = editingItem(t, s, id, owner)
	oldCode := item.Gap.Code
	input = editInput(item, "进一步整理的科研方法与复核范围说明")
	input.Category = "化学"
	mustStatus(t, request(s, "POST", path, input, owner), 200)
	rev = editingItem(t, s, id, owner).Revision
	if rev.Draft.Code != oldCode {
		t.Fatal("draft code changed before approval")
	}
	approval["level"], approval["edit_token"] = "ordinary", rev.EditToken
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/revisions/%d/review", rev.ID), approval, admin), 200)
	item = editingItem(t, s, id, owner)
	if item.Gap.Code != oldCode || item.Gap.Category != "化学" || item.Gap.Level != "ordinary" || item.Gap.SourceURL != unchanged.Gap.SourceURL || item.Gap.SourceVisibility != unchanged.Gap.SourceVisibility {
		t.Fatal("revision lost stable code or original provenance")
	}
}

func TestRevisionOldSnapshotRespectsNewInternalSourcePrivacy(t *testing.T) {
	s := testServer(t)
	ownerID, owner := account(t, s, "changed-privacy@example.org", "member", 0)
	_, admin := account(t, s, founderEmail, "admin", 0)
	id := gap(t, s, "ordinary", "published", ownerID)
	item := editingItem(t, s, id, owner)
	input := editInput(item, "新的研究方法与证据适用范围说明")
	input.Body += "原始记录来自 Nature，链接 " + item.Gap.SourceURL
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/submissions/%d/edit", id), input, owner), 200)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/review/%d", id), map[string]string{"action": "expire", "note": "资料过期需要重新核验"}, admin), 200)
	legacy := map[string]any{"title": input.Title, "summary": input.Summary, "body": input.Body, "category": input.Category, "evidence": "unverified", "tags": input.Tags, "source_visibility": "internal"}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/gaps/%d", id), legacy, admin), 200)
	item = editingItem(t, s, id, owner)
	raw, _ := json.Marshal(item)
	if strings.Contains(strings.ToLower(string(raw)), "nature") || item.Revision.Draft.SourceVisibility != "internal" {
		t.Fatal("historical revision leaked a now-private source", string(raw))
	}
	staff := editingItem(t, s, id, admin)
	if staff.Revision.Draft.SourceURL == "" || !strings.Contains(staff.Revision.Draft.Body, "Nature") {
		t.Fatal("redaction modified the stored review snapshot")
	}
}

func TestSubmissionEditMigrationAndSourceFieldTampering(t *testing.T) {
	s := testServer(t)
	ownerID, owner := account(t, s, "migration-author@example.org", "member", 0)
	id := gap(t, s, "ordinary", "published", ownerID)
	before, err := s.oneGap(id, &User{Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"edit_version", "stable_code"} {
		if _, err := s.db.Exec("ALTER TABLE gaps DROP COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := initSubmissionEdits(s.db); err != nil {
			t.Fatal(err)
		}
	}
	item := editingItem(t, s, id, owner)
	if item.Gap.Code != before.Code || item.Gap.Body != before.Body {
		t.Fatal("migration changed historical content")
	}
	input := editInput(item, "新的核验结论与实验方法适用限制")
	raw, _ := json.Marshal(input)
	var tampered map[string]any
	json.Unmarshal(raw, &tampered)
	tampered["source_visibility"] = "public"
	tampered["source_url"] = "https://example.org/replaced"
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/submissions/%d/edit", id), tampered, owner), 400)
	if editingItem(t, s, id, owner).Revision != nil {
		t.Fatal("source tampering created revision")
	}
}
