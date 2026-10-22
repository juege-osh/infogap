package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// This command is available only to the separately constrained SSH reviewer.
// It never accepts SQL, shell arguments, account fields or payment actions.
type submissionBridgeRequest struct {
	Action                 string            `json:"action"`
	ID                     int64             `json:"id"`
	ExpectedUpdatedAt      string            `json:"expected_updated_at"`
	ExpectedSubmissionHash string            `json:"expected_submission_hash"`
	Decision               string            `json:"decision"`
	Level                  string            `json:"level"`
	Note                   string            `json:"note"`
	Checks                 []submissionCheck `json:"checks"`
	SourceHash             string            `json:"source_hash"`
}
type submissionCheck struct {
	Claim string `json:"claim"`
	Quote string `json:"quote"`
}

func submissionHash(g *Gap) string {
	b, _ := json.Marshal([]any{g.ID, g.Title, g.Summary, g.Body, g.Category, g.Level, g.Evidence, g.SourceName, g.SourceURL, g.SourceVisibility, g.PublishedAt, g.Tags, g.AuthorID.Int64})
	return digest(string(b))
}
func (s *server) initSubmissionReview() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS submission_reviews(id INTEGER PRIMARY KEY AUTOINCREMENT,gap_id INTEGER NOT NULL REFERENCES gaps(id),snapshot_hash TEXT NOT NULL,decision TEXT NOT NULL,note TEXT NOT NULL,source_hash TEXT NOT NULL DEFAULT '',checks TEXT NOT NULL DEFAULT '[]',created_at TEXT NOT NULL,notified_at TEXT NOT NULL DEFAULT '',UNIQUE(gap_id,snapshot_hash));`)
	return err
}

// The reviewer process deliberately skips InitBilling and all account/payment
// migrations. It opens the already migrated production database only.
func newSubmissionReviewServer(dbPath, origin string) (*server, error) {
	if dbPath == "" || dbPath == ":memory:" {
		return nil, errors.New("existing_database_required")
	}
	if _, err := os.Stat(filepath.Clean(dbPath)); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=10000;"); err != nil {
		db.Close()
		return nil, err
	}
	return &server{db: db, origin: origin, secure: strings.HasPrefix(origin, "https://"), now: time.Now}, nil
}
func runSubmissionReviewBridge(s *server, in io.Reader, out io.Writer) error {
	if err := s.initSubmissionReview(); err != nil {
		return err
	}
	var req submissionBridgeRequest
	d := json.NewDecoder(io.LimitReader(in, 128*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		return errors.New("invalid_submission_request")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing_submission_request")
	}
	var result any
	var err error
	switch req.Action {
	case "export":
		if err = s.retrySubmissionNotifications(); err != nil {
			return err
		}
		result, err = s.exportSubmissions()
	case "review":
		result, err = s.applySubmissionReview(req)
	default:
		return errors.New("submission_action_not_allowed")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
func (s *server) exportSubmissions() (any, error) {
	reviewed := map[int64]map[string]bool{}
	previous, err := s.db.Query("SELECT sr.gap_id,sr.snapshot_hash FROM submission_reviews sr JOIN gaps g ON g.id=sr.gap_id WHERE g.status='pending'")
	if err != nil {
		return nil, err
	}
	for previous.Next() {
		var id int64
		var hash string
		if err = previous.Scan(&id, &hash); err != nil {
			previous.Close()
			return nil, err
		}
		if reviewed[id] == nil {
			reviewed[id] = map[string]bool{}
		}
		reviewed[id][hash] = true
	}
	err = previous.Err()
	previous.Close()
	if err != nil {
		return nil, err
	}
	var ownerID int64
	_ = s.db.QueryRow("SELECT id FROM users WHERE email=? AND role='admin'", founderEmail).Scan(&ownerID)
	rows, err := s.db.Query(gapSelect+`WHERE g.status='pending' AND g.author_id IS NOT NULL AND u.disabled=0 AND g.author_id NOT IN(SELECT user_id FROM test_accounts) ORDER BY g.created_at,g.id`, 0, 0)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		g, e := gapScan(rows, &User{Role: "admin"})
		if e != nil {
			return nil, e
		}
		hash := submissionHash(g)
		if reviewed[g.ID][hash] {
			continue
		}
		items = append(items, map[string]any{"id": g.ID, "code": g.Code, "title": g.Title, "summary": g.Summary, "body": g.Body, "category": g.Category, "level": g.Level, "evidence": g.Evidence, "source_name": g.SourceName, "source_url": g.SourceURL, "published_at": g.PublishedAt, "tags": g.Tags, "updated_at": g.UpdatedAt, "submission_hash": hash, "owner_submission": g.AuthorID.Int64 == ownerID})
		if len(items) >= 200 {
			break
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	var notificationsPending int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM submission_reviews sr JOIN gaps g ON g.id=sr.gap_id WHERE sr.decision='hold' AND sr.notified_at='' AND g.status='pending'").Scan(&notificationsPending)
	return map[string]any{"items": items, "total": len(items), "notifications_pending": notificationsPending}, nil
}

func (s *server) applySubmissionReview(req submissionBridgeRequest) (any, error) {
	if req.ID <= 0 || len(req.ExpectedSubmissionHash) != 64 || req.ExpectedUpdatedAt == "" || utf8.RuneCountInString(req.Note) < 10 || utf8.RuneCountInString(req.Note) > 1000 {
		return nil, errors.New("invalid_submission_review")
	}
	if !has([]string{"approve", "reject", "hold"}, req.Decision) {
		return nil, errors.New("invalid_submission_decision")
	}
	g, err := gapScan(s.db.QueryRow(gapSelect+"WHERE g.id=?", 0, 0, req.ID), &User{Role: "admin"})
	if err != nil {
		return nil, err
	}
	if !g.AuthorID.Valid || g.Status != "pending" || submissionHash(g) != req.ExpectedSubmissionHash || g.UpdatedAt != req.ExpectedUpdatedAt {
		return nil, errors.New("submission_changed_or_not_pending")
	}
	var disabled int
	var isOwner bool
	if err = s.db.QueryRow("SELECT disabled,email=? FROM users WHERE id=?", founderEmail, g.AuthorID.Int64).Scan(&disabled, &isOwner); err != nil {
		return nil, err
	}
	if disabled != 0 {
		return nil, errors.New("submission_author_disabled")
	}
	if isOwner {
		req.Decision = "hold"
		req.Note = "站长本人提交的稿件需另行确认，已保留待站长审核，系统不自动审批自己的投稿。"
	}
	var test int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM test_accounts WHERE user_id=?", g.AuthorID.Int64).Scan(&test); err != nil || test > 0 {
		return nil, errors.New("test_submission_not_allowed")
	}
	if req.Decision == "approve" {
		if !has([]string{"ordinary", "featured"}, req.Level) || len(req.SourceHash) != 64 || len(req.Checks) < 2 || len(req.Checks) > 5 {
			return nil, errors.New("submission_evidence_required")
		}
		for _, c := range req.Checks {
			if utf8.RuneCountInString(c.Claim) < 5 || utf8.RuneCountInString(c.Quote) < 10 {
				return nil, errors.New("submission_check_required")
			}
		}
	}
	checks, _ := json.Marshal(req.Checks)
	at := stamp(s.now())
	if req.Decision == "hold" {
		tx, e := s.db.Begin()
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		g, e = gapScan(tx.QueryRow(gapSelect+"WHERE g.id=?", 0, 0, req.ID), &User{Role: "admin"})
		if e != nil {
			return nil, e
		}
		if !g.AuthorID.Valid || g.Status != "pending" || submissionHash(g) != req.ExpectedSubmissionHash || g.UpdatedAt != req.ExpectedUpdatedAt {
			return nil, errors.New("submission_changed_or_not_pending")
		}
		_, e = tx.Exec("UPDATE gaps SET review_note=?,updated_at=? WHERE id=?", "待站长审核："+req.Note, at, g.ID)
		if e != nil {
			return nil, e
		}
		_, e = tx.Exec("INSERT INTO submission_reviews(gap_id,snapshot_hash,decision,note,source_hash,checks,created_at) VALUES(?,?,?,?,?,?,?)", g.ID, req.ExpectedSubmissionHash, req.Decision, req.Note, req.SourceHash, string(checks), at)
		if e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
		return s.notifySubmissionHold(g, req.ExpectedSubmissionHash, req.Note)
	}
	// Authenticate the exact existing review handler with a short lived local
	// session. The token is never returned, stored in logs or sent over SSH.
	var reviewer int64
	if err = s.db.QueryRow("SELECT id FROM users WHERE email=? AND role='admin' AND disabled=0", founderEmail).Scan(&reviewer); err != nil {
		return nil, errors.New("founder_account_unavailable")
	}
	if reviewer == g.AuthorID.Int64 {
		return nil, errors.New("cannot_review_own_submission")
	}
	tok := token(32)
	hash := digest(tok)
	if _, err = s.db.Exec("INSERT INTO sessions(token_hash,user_id,created_at,expires_at) VALUES(?,?,?,?)", hash, reviewer, at, stamp(s.now().Add(time.Minute))); err != nil {
		return nil, err
	}
	defer s.db.Exec("DELETE FROM sessions WHERE token_hash=?", hash)
	payload, _ := json.Marshal(map[string]any{"action": req.Decision, "level": req.Level, "note": "自动审核：" + req.Note, "expected_updated_at": req.ExpectedUpdatedAt, "expected_submission_hash": req.ExpectedSubmissionHash})
	r := httptest.NewRequest(http.MethodPost, "http://localhost/api/admin/review/"+strconv.FormatInt(g.ID, 10), bytes.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: "infogap_session", Value: tok})
	r.SetPathValue("id", strconv.FormatInt(g.ID, 10))
	w := httptest.NewRecorder()
	s.review(w, r)
	if w.Code != 200 {
		return nil, fmt.Errorf("submission_review_http_%d", w.Code)
	}
	if _, err = s.db.Exec("INSERT INTO submission_reviews(gap_id,snapshot_hash,decision,note,source_hash,checks,created_at) VALUES(?,?,?,?,?,?,?)", g.ID, req.ExpectedSubmissionHash, req.Decision, req.Note, req.SourceHash, string(checks), at); err != nil {
		return nil, err
	}
	return map[string]any{"id": g.ID, "decision": req.Decision, "applied": true}, nil
}
func (s *server) notifySubmissionHold(g *Gap, hash, note string) (any, error) {
	// E-mail contains no submitted links or HTML. The owner opens the known
	// admin page to inspect the original evidence in a controlled context.
	title := strings.ReplaceAll(strings.ReplaceAll(g.Title, "\r", " "), "\n", " ")
	note = hideSubmissionLinks(note)
	title = hideSubmissionLinks(title)
	body := fmt.Sprintf("觉哥，有一条投稿需要你确认。\n\n编号：%s\n标题：%s\n原因：%s\n\n这条投稿仍是待审核状态，尚未公开，也没有发放奖励。\n请打开管理后台处理：https://infogap.osh.lol/#admin\n", g.Code, title, note)
	if err := submissionNotificationMail(context.Background(), founderEmail, "InfoGap 投稿待站长审核", body); err != nil {
		return map[string]any{"id": g.ID, "decision": "hold", "applied": true, "notification": "failed"}, nil
	}
	_, err := s.db.Exec("UPDATE submission_reviews SET notified_at=? WHERE gap_id=? AND snapshot_hash=?", stamp(s.now()), g.ID, hash)
	return map[string]any{"id": g.ID, "decision": "hold", "applied": true, "notification": "sent"}, err
}

// Retry failed notifications once per scheduled run, without re-reviewing or
// rewarding the submission again. Replaced/handled drafts are not notified.
func (s *server) retrySubmissionNotifications() error {
	rows, e := s.db.Query("SELECT gap_id,snapshot_hash,note FROM submission_reviews WHERE decision='hold' AND notified_at='' ORDER BY id LIMIT 20")
	if e != nil {
		return e
	}
	type pending struct {
		id         int64
		hash, note string
	}
	items := []pending{}
	for rows.Next() {
		var p pending
		if e = rows.Scan(&p.id, &p.hash, &p.note); e != nil {
			rows.Close()
			return e
		}
		items = append(items, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, p := range items {
		g, err := gapScan(s.db.QueryRow(gapSelect+"WHERE g.id=? AND g.status='pending'", 0, 0, p.id), &User{Role: "admin"})
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		if submissionHash(g) != p.hash {
			continue
		}
		if _, err = s.notifySubmissionHold(g, p.hash, p.note); err != nil {
			return err
		}
	}
	return nil
}

func hideSubmissionLinks(text string) string {
	parts := strings.Fields(text)
	for i, p := range parts {
		if strings.Contains(p, "://") {
			parts[i] = "【链接已隐藏，请在后台核对】"
		}
	}
	return strings.Join(parts, " ")
}
