package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func courseFixture(t *testing.T) (*server, int64, string, int64) {
	t.Helper()
	t.Setenv("INFOGAP_COURSE_MIN_FREE_BYTES", "0")
	s := testServer(t)
	adminID, admin := account(t, s, founderEmail, "admin", 0)
	w := request(s, "POST", "/api/admin/courses", map[string]any{"title": "Codex 入门课程", "summary": "学习操作与实际案例", "topic": "codex", "price_cents": 9900}, admin)
	mustStatus(t, w, 201)
	var out struct {
		Course Course `json:"course"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	return s, adminID, admin, out.Course.ID
}
func courseMP4Fixture(size int) []byte {
	if size < 48 {
		size = 48
	}
	b := make([]byte, size)
	binary.BigEndian.PutUint32(b[:4], 24)
	copy(b[4:8], "ftyp")
	copy(b[8:12], "isom")
	copy(b[16:24], "isommp42")
	binary.BigEndian.PutUint32(b[24:28], 9)
	copy(b[28:32], "moov")
	binary.BigEndian.PutUint32(b[33:37], uint32(size-33))
	copy(b[37:41], "mdat")
	for i := 41; i < size; i++ {
		b[i] = byte(i % 251)
	}
	return b
}
func courseStart(t *testing.T, s *server, cookie string, cid int64, key string, sizes ...int64) []courseUploadReceipt {
	t.Helper()
	files := []courseUploadFile{}
	for i, size := range sizes {
		files = append(files, courseUploadFile{Name: fmt.Sprintf("%d.mp4", i), Title: fmt.Sprintf("第%d课", i+1), Size: size, ContentType: "video/mp4"})
	}
	w := request(s, "POST", fmt.Sprintf("/api/admin/courses/%d/uploads", cid), map[string]any{"batch_key": key, "files": files}, cookie)
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("start %d %s", w.Code, w.Body)
	}
	var out struct {
		Uploads []courseUploadReceipt `json:"uploads"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	return out.Uploads
}
func coursePart(s *server, cookie, id string, index int, b []byte, hash string, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PUT", fmt.Sprintf("http://example.com/api/admin/course-uploads/%s/parts/%d", id, index), bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/octet-stream")
	r.Header.Set("Origin", origin)
	r.Header.Set("X-Content-SHA256", hash)
	if cookie != "" {
		r.Header.Set("Cookie", "infogap_session="+cookie)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}
func courseHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func courseFinish(t *testing.T, s *server, cookie string, v courseUploadReceipt, b []byte) CourseLesson {
	t.Helper()
	hashes := []string{}
	for i := 0; i < v.PartCount; i++ {
		start := int64(i) * coursePartSize
		end := min(start+coursePartSize, int64(len(b)))
		part := b[start:end]
		h := courseHash(part)
		mustStatus(t, coursePart(s, cookie, v.ID, i, part, h, "http://example.com"), 200)
		hashes = append(hashes, h)
	}
	w := request(s, "POST", "/api/admin/course-uploads/"+v.ID+"/complete", map[string]any{"part_hashes": hashes}, cookie)
	mustStatus(t, w, 200)
	var out struct {
		Lesson CourseLesson `json:"lesson"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	return out.Lesson
}
func coursePublish(t *testing.T, s *server, cookie string, cid int64) {
	t.Helper()
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/courses/%d", cid), map[string]any{"title": "Codex 入门课程", "summary": "课程介绍", "topic": "codex", "price_cents": 9900, "status": "published"}, cookie), 200)
}
func TestCourseDraftPricingAndAuthorization(t *testing.T) {
	s, _, admin, cid := courseFixture(t)
	_, member := account(t, s, "course-member@example.org", "member", 100000)
	_, reviewer := account(t, s, "course-reviewer@example.org", "reviewer", 1000)
	mustStatus(t, request(s, "GET", fmt.Sprintf("/api/courses/%d", cid), nil, ""), 404)
	mustStatus(t, request(s, "GET", fmt.Sprintf("/api/courses/%d", cid), nil, admin), 200)
	for _, cookie := range []string{member, reviewer} {
		mustStatus(t, request(s, "POST", "/api/admin/courses", map[string]any{"title": "No permission", "topic": "codex"}, cookie), 403)
		mustStatus(t, request(s, "GET", "/api/admin/courses", nil, cookie), 403)
	}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/courses/%d", cid), map[string]any{"title": "No ready video", "topic": "codex", "price_cents": 100, "status": "published"}, admin), 409)
	if _, e := s.courseBillingPlan(2, cid); e != errCourseNotFound {
		t.Fatal(e)
	}
	w := request(s, "GET", "/api/courses", nil, member)
	mustStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "Codex 入门课程") {
		t.Fatal("draft leaked")
	}
}
func TestCourseChunkOrderReplayAndRangeAccess(t *testing.T) {
	s, _, admin, cid := courseFixture(t)
	payerID, payer := account(t, s, "course-payer@example.org", "member", 1000)
	_, outsider := account(t, s, "course-other@example.org", "reviewer", 100000)
	data := courseMP4Fixture(int(coursePartSize) + 52)
	up := courseStart(t, s, admin, cid, "course_batch_order_01", int64(len(data)), 48)
	again := courseStart(t, s, admin, cid, "course_batch_order_01", int64(len(data)), 48)
	if len(again) != 2 || again[0].ID != up[0].ID {
		t.Fatal("batch retry duplicated")
	}
	second := courseFinish(t, s, admin, up[1], courseMP4Fixture(48))
	first := courseFinish(t, s, admin, up[0], data)
	if first.Position != 1 || second.Position != 2 {
		t.Fatal("completion changed order")
	}
	coursePublish(t, s, admin, cid)
	for _, cookie := range []string{"", payer, outsider} {
		w := request(s, "GET", fmt.Sprintf("/api/courses/%d", cid), nil, cookie)
		mustStatus(t, w, 200)
		if strings.Contains(w.Body.String(), "media_url") || strings.Contains(w.Body.String(), s.courseDir) {
			t.Fatal("unowned path leaked", w.Body)
		}
	}
	mustStatus(t, request(s, "GET", first.MediaURL, nil, ""), 401)
	mustStatus(t, request(s, "GET", first.MediaURL, nil, payer), 403)
	mustStatus(t, request(s, "GET", first.MediaURL, nil, outsider), 403)
	f := &fakeBillingProvider{}
	s.billing = &billingService{provider: f, merchantID: "test-merchant"}
	o := billingPurchase(t, s, payer, fmt.Sprintf("course_%d", cid), "alipay")
	mustStatus(t, billingCallback(s, billingSuccess(o), "test-valid"), 200)
	var credits int
	if e := s.db.QueryRow(`SELECT credits FROM users WHERE id=?`, payerID).Scan(&credits); e != nil || credits != 1000 {
		t.Fatal("course changed credits", e, credits)
	}
	w := request(s, "GET", fmt.Sprintf("/api/courses/%d", cid), nil, payer)
	mustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"can_watch":true`) || !strings.Contains(w.Body.String(), "media_url") {
		t.Fatal(w.Body)
	}
	for _, cookie := range []string{payer, admin} {
		r := httptest.NewRequest("GET", "http://example.com"+first.MediaURL, nil)
		r.Header.Set("Cookie", "infogap_session="+cookie)
		r.Header.Set("Range", "bytes=0-23")
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		mustStatus(t, w, 206)
		if !bytes.Equal(w.Body.Bytes(), data[:24]) || w.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-23/%d", len(data)) || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("bad range", w.Header())
		}
	}
	r := httptest.NewRequest("HEAD", "http://example.com"+first.MediaURL, nil)
	r.Header.Set("Cookie", "infogap_session="+payer)
	w = httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	mustStatus(t, w, 200)
	if w.Body.Len() != 0 {
		t.Fatal("HEAD has body")
	}
	r = httptest.NewRequest("GET", "http://example.com"+first.MediaURL, nil)
	r.Header.Set("Cookie", "infogap_session="+payer)
	r.Header.Set("Range", "bytes=999999999-")
	w = httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	mustStatus(t, w, 416)
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("range error cacheable")
	}
	_, _ = s.db.Exec(`UPDATE courses SET status='draft',price_cents=0 WHERE id=?`, cid)
	mustStatus(t, request(s, "GET", first.MediaURL, nil, payer), 200)
	mustStatus(t, request(s, "GET", fmt.Sprintf("/api/courses/%d", cid), nil, payer), 200)
	mustStatus(t, request(s, "GET", fmt.Sprintf("/api/courses/%d", cid), nil, outsider), 404)
	hashes := []string{courseHash(data[:coursePartSize]), courseHash(data[coursePartSize:])}
	mustStatus(t, request(s, "POST", "/api/admin/course-uploads/"+up[0].ID+"/complete", map[string]any{"part_hashes": hashes}, admin), 200)
	var lessons int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM course_lessons WHERE course_id=?`, cid).Scan(&lessons)
	if lessons != 2 {
		t.Fatal("completion duplicated")
	}
}
func TestCourseChunkValidationAndNoExposure(t *testing.T) {
	s, _, admin, cid := courseFixture(t)
	_, member := account(t, s, "chunkmember@example.org", "member", 100)
	b := courseMP4Fixture(48)
	up := courseStart(t, s, admin, cid, "course_chunk_validation", 48)[0]
	h := courseHash(b)
	mustStatus(t, coursePart(s, admin, up.ID, 0, b, h, "https://other.example"), 403)
	mustStatus(t, coursePart(s, member, up.ID, 0, b, h, "http://example.com"), 403)
	mustStatus(t, coursePart(s, admin, up.ID, 1, b, h, "http://example.com"), 400)
	mustStatus(t, coursePart(s, admin, up.ID, 0, b[:47], h, "http://example.com"), 400)
	mustStatus(t, coursePart(s, admin, up.ID, 0, b, strings.Repeat("0", 64), "http://example.com"), 400)
	mustStatus(t, request(s, "POST", "/api/admin/course-uploads/"+up.ID+"/complete", map[string]any{"part_hashes": []string{h}}, admin), 409)
	mustStatus(t, coursePart(s, admin, up.ID, 0, b, h, "http://example.com"), 200)
	mustStatus(t, coursePart(s, admin, up.ID, 0, b, h, "http://example.com"), 200)
	changed := append([]byte(nil), b...)
	changed[47]++
	mustStatus(t, coursePart(s, admin, up.ID, 0, changed, courseHash(changed), "http://example.com"), 409)
	mustStatus(t, request(s, "POST", "/api/admin/course-uploads/"+up.ID+"/complete", map[string]any{"part_hashes": []string{courseHash(changed)}}, admin), 409)
	files, _ := filepath.Glob(filepath.Join(s.courseDir, "uploads", up.ID, "*.tmp"))
	if len(files) != 0 {
		t.Fatal("failed write left temp files")
	}
	mustStatus(t, request(s, "GET", "/uploads/"+up.ID+"/0.part", nil, member), 404)
	invalid := bytes.Repeat([]byte("<html>"), 8)
	bad := courseStart(t, s, admin, cid, "course_bad_video_validation", int64(len(invalid)))[0]
	mustStatus(t, coursePart(s, admin, bad.ID, 0, invalid, courseHash(invalid), "http://example.com"), 200)
	mustStatus(t, request(s, "POST", "/api/admin/course-uploads/"+bad.ID+"/complete", map[string]any{"part_hashes": []string{courseHash(invalid)}}, admin), 400)
	files, _ = filepath.Glob(filepath.Join(s.courseDir, "media", "*"))
	if len(files) != 0 {
		t.Fatal("invalid video retained media")
	}
}
func TestCourseQuotaExpireAndAtomicUpload(t *testing.T) {
	s, _, admin, cid := courseFixture(t)
	t.Setenv("INFOGAP_COURSE_MAX_BYTES", "48")
	up := courseStart(t, s, admin, cid, "course_quota_batch", 48)[0]
	payload := map[string]any{"batch_key": "course_over_quota_batch", "files": []courseUploadFile{{Name: "x.mp4", Title: "第二课", Size: 48, ContentType: "video/mp4"}}}
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/courses/%d/uploads", cid), payload, admin), 507)
	b := courseMP4Fixture(48)
	mustStatus(t, coursePart(s, admin, up.ID, 0, b, courseHash(b), "http://example.com"), 200)
	_, _ = s.db.Exec(`UPDATE course_uploads SET expires_at=? WHERE id=?`, stamp(s.now().Add(-time.Second)), up.ID)
	mustStatus(t, coursePart(s, admin, up.ID, 0, b, courseHash(b), "http://example.com"), 409)
	next := courseStart(t, s, admin, cid, "course_after_expire_batch", 48)[0]
	if next.Position != 2 {
		t.Fatal("positions reused")
	}
	if _, e := os.Stat(filepath.Join(s.courseDir, "uploads", up.ID)); !os.IsNotExist(e) {
		t.Fatal("expired chunks retained", e)
	}
	// Audit failure rolls back publication and removes the newly merged media file.
	mustStatus(t, coursePart(s, admin, next.ID, 0, b, courseHash(b), "http://example.com"), 200)
	_, e := s.db.Exec(`CREATE TRIGGER reject_course_ready BEFORE INSERT ON audit WHEN NEW.action='course_video_ready' BEGIN SELECT RAISE(ABORT,'fixture'); END`)
	if e != nil {
		t.Fatal(e)
	}
	mustStatus(t, request(s, "POST", "/api/admin/course-uploads/"+next.ID+"/complete", map[string]any{"part_hashes": []string{courseHash(b)}}, admin), 500)
	var status string
	_ = s.db.QueryRow(`SELECT status FROM course_lessons WHERE id=?`, next.LessonID).Scan(&status)
	if status != "uploading" {
		t.Fatal("failed commit changed lesson")
	}
	files, _ := filepath.Glob(filepath.Join(s.courseDir, "media", "*"))
	if len(files) != 0 {
		t.Fatal("rollback left published file")
	}
}
func TestCourseConcurrentSamePartAndPrivateRoot(t *testing.T) {
	s, _, admin, cid := courseFixture(t)
	b := courseMP4Fixture(48)
	up := courseStart(t, s, admin, cid, "course_concurrent_batch", 48)[0]
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- coursePart(s, admin, up.ID, 0, b, courseHash(b), "http://example.com").Code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatal(code)
		}
	}
	var count int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM course_upload_parts WHERE upload_id=?`, up.ID).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate part")
	}
	old := s.courseDir
	s.courseDir = filepath.Join(s.web, "courses-test-private")
	defer os.RemoveAll(s.courseDir)
	if _, e := s.courseStorage(); e == nil {
		t.Fatal("public root allowed")
	}
	s.courseDir = old
}

func TestCourseReadingAndTestModeNeverGrantVideos(t *testing.T) {
	s, _, admin, cid := courseFixture(t)
	up := courseStart(t, s, admin, cid, "course_reading_independent", 48)[0]
	lesson := courseFinish(t, s, admin, up, courseMP4Fixture(48))
	coursePublish(t, s, admin, cid)
	memberID, member := account(t, s, "reading-no-course@example.org", "member", 100000)
	s.billing = &billingService{provider: &fakeBillingProvider{}, merchantID: "test-merchant"}
	order := billingPurchase(t, s, member, "reading_12", "alipay")
	mustStatus(t, billingCallback(s, billingSuccess(order), "test-valid"), 200)
	u := previewUser(t, s, member)
	if u.ID != memberID || !u.Entitlements.UnlimitedReading {
		t.Fatal("reading fixture not entitled")
	}
	mustStatus(t, request(s, "GET", lesson.MediaURL, nil, member), 403)
	mustStatus(t, request(s, "POST", "/api/admin/test-mode", map[string]any{"tier": 9, "credits": 100000, "member": true, "reset": true}, admin), 200)
	mustStatus(t, request(s, "GET", lesson.MediaURL, nil, admin), 403)
	mustStatus(t, request(s, "GET", "/api/admin/courses", nil, admin), 403)
	mustStatus(t, request(s, "POST", fmt.Sprintf("/api/admin/courses/%d/uploads", cid), map[string]any{}, admin), 403)
	b := courseMP4Fixture(48)
	mustStatus(t, coursePart(s, admin, up.ID, 0, b, courseHash(b), "http://example.com"), 403)
	mustStatus(t, request(s, "POST", "/api/billing/orders", map[string]any{"plan": fmt.Sprintf("course_%d", cid), "channel": "alipay"}, admin), 403)
}

func TestCourseInvalidManifestAndInitializationRollback(t *testing.T) {
	s, _, admin, cid := courseFixture(t)
	path := fmt.Sprintf("/api/admin/courses/%d/uploads", cid)
	for _, f := range []courseUploadFile{
		{Name: "../x.mp4", Title: "第一课", Size: 48, ContentType: "video/mp4"},
		{Name: "x.mp4", Title: "第一课", Size: courseMaxFile + 1, ContentType: "video/mp4"},
		{Name: "x.html", Title: "第一课", Size: 48, ContentType: "video/mp4"},
		{Name: "x.mp4", Title: "第一课", Size: 48, ContentType: "text/html"},
	} {
		mustStatus(t, request(s, "POST", path, map[string]any{"batch_key": "course_invalid_manifest", "files": []courseUploadFile{f}}, admin), 400)
	}
	files := make([]courseUploadFile, 31)
	for i := range files {
		files[i] = courseUploadFile{Name: "x.mp4", Title: "第一课", Size: 48, ContentType: "video/mp4"}
	}
	mustStatus(t, request(s, "POST", path, map[string]any{"batch_key": "course_invalid_manifest", "files": files}, admin), 400)
	_, e := s.db.Exec(`CREATE TRIGGER reject_course_batch BEFORE INSERT ON audit WHEN NEW.action='course_upload_batch' BEGIN SELECT RAISE(ABORT,'fixture'); END`)
	if e != nil {
		t.Fatal(e)
	}
	mustStatus(t, request(s, "POST", path, map[string]any{"batch_key": "course_rollback_manifest", "files": files[:1]}, admin), 500)
	for _, table := range []string{"course_uploads", "course_upload_batches", "course_lessons"} {
		var n int
		if e = s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); e != nil || n != 0 {
			t.Fatal("partial upload state", table, n, e)
		}
	}
}
