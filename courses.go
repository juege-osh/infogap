package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	errCourseNotFound       = errors.New("课程不存在或尚未上架")
	errCourseNotPurchasable = errors.New("课程尚未定价或没有可观看的视频")
	errCourseAlreadyOwned   = errors.New("已购买该课程")
)

const courseSchema = `
CREATE TABLE IF NOT EXISTS courses (
 id INTEGER PRIMARY KEY AUTOINCREMENT,title TEXT NOT NULL,summary TEXT NOT NULL DEFAULT '',topic TEXT NOT NULL CHECK(topic='codex'),
 price_cents INTEGER NOT NULL DEFAULT 0 CHECK(price_cents>=0),status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','published')),
 created_by INTEGER NOT NULL REFERENCES users(id),created_at TEXT NOT NULL,updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS course_lessons (
 id INTEGER PRIMARY KEY AUTOINCREMENT,course_id INTEGER NOT NULL REFERENCES courses(id),title TEXT NOT NULL,position INTEGER NOT NULL CHECK(position>0),
 bytes INTEGER NOT NULL CHECK(bytes>0),status TEXT NOT NULL DEFAULT 'uploading' CHECK(status IN ('uploading','ready','failed')),
 media_key TEXT NOT NULL DEFAULT '',sha256 TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,UNIQUE(course_id,position)
);
CREATE TABLE IF NOT EXISTS course_access (
 user_id INTEGER NOT NULL REFERENCES users(id),course_id INTEGER NOT NULL REFERENCES courses(id),order_id TEXT NOT NULL UNIQUE REFERENCES billing_orders(id),
 created_at TEXT NOT NULL,PRIMARY KEY(user_id,course_id)
);
CREATE TABLE IF NOT EXISTS course_upload_batches (
 batch_key TEXT PRIMARY KEY,course_id INTEGER NOT NULL REFERENCES courses(id),actor_id INTEGER NOT NULL REFERENCES users(id),
 manifest_hash TEXT NOT NULL,created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS course_uploads (
 id TEXT PRIMARY KEY,lesson_id INTEGER NOT NULL UNIQUE REFERENCES course_lessons(id),batch_key TEXT NOT NULL REFERENCES course_upload_batches(batch_key),
 expected_size INTEGER NOT NULL CHECK(expected_size>0),part_count INTEGER NOT NULL CHECK(part_count>0),status TEXT NOT NULL DEFAULT 'uploading' CHECK(status IN ('uploading','complete','expired')),
 created_at TEXT NOT NULL,expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS course_upload_parts (
 upload_id TEXT NOT NULL REFERENCES course_uploads(id),part_index INTEGER NOT NULL CHECK(part_index>=0),bytes INTEGER NOT NULL CHECK(bytes>0),sha256 TEXT NOT NULL,
 PRIMARY KEY(upload_id,part_index)
);
CREATE INDEX IF NOT EXISTS course_lesson_order ON course_lessons(course_id,position);
`

func initCourses(db *sql.DB) error { _, err := db.Exec(courseSchema); return err }

func (s *server) courseBillingPlan(userID, courseID int64) (*billingPlan, error) {
	var title, status string
	var price int64
	var ready, owned int
	err := s.db.QueryRow(`SELECT c.title,c.status,c.price_cents,(SELECT COUNT(*) FROM course_lessons WHERE course_id=c.id AND status='ready'),EXISTS(SELECT 1 FROM course_access WHERE course_id=c.id AND user_id=?) FROM courses c WHERE c.id=?`, userID, courseID).Scan(&title, &status, &price, &ready, &owned)
	if err == sql.ErrNoRows || err == nil && status != "published" {
		return nil, errCourseNotFound
	}
	if err != nil {
		return nil, err
	}
	if owned != 0 {
		return nil, errCourseAlreadyOwned
	}
	if price <= 0 || price > 100000000 || ready == 0 {
		return nil, errCourseNotPurchasable
	}
	return &billingPlan{ID: fmt.Sprintf("course_%d", courseID), Name: title, Type: "course", CourseID: courseID, AmountCents: price, ListAmountCents: price, DiscountPercent: 100}, nil
}

func grantCourseAccess(tx *sql.Tx, o *billingOrder, now time.Time) error {
	if o.Type != "course" || o.CourseID <= 0 || o.UserID <= 0 || o.Plan != fmt.Sprintf("course_%d", o.CourseID) || o.AmountCents <= 0 || o.Days != 0 || o.Credits != 0 || o.RankTier != 0 || o.Months != 0 || o.MonthlyCredits != 0 || o.ReadingPolicyVersion != 0 || o.DelayHours != 0 || o.UnlimitedReading || o.ListAmountCents != o.AmountCents || o.DiscountPercent != 100 || o.RankSnapshot != "" {
		return errors.New("invalid course order snapshot")
	}
	// A late verified payment honors its price snapshot, including after unpublishing.
	// A second independently paid order never revokes the already-owned course.
	_, err := tx.Exec(`INSERT INTO course_access(user_id,course_id,order_id,created_at) VALUES(?,?,?,?) ON CONFLICT(user_id,course_id) DO NOTHING`, o.UserID, o.CourseID, o.ID, stamp(now))
	return err
}

type Course struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Topic       string `json:"topic"`
	PriceCents  int64  `json:"price_cents"`
	Status      string `json:"status"`
	LessonCount int    `json:"lesson_count"`
	Purchased   bool   `json:"purchased"`
	CanWatch    bool   `json:"can_watch"`
	CanBuy      bool   `json:"can_buy"`
}
type CourseLesson struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Position int    `json:"position"`
	Bytes    int64  `json:"bytes"`
	Status   string `json:"status"`
	MediaURL string `json:"media_url,omitempty"`
}

const courseSelect = `SELECT c.id,c.title,c.summary,c.topic,c.price_cents,c.status,(SELECT COUNT(*) FROM course_lessons WHERE course_id=c.id AND status='ready'),EXISTS(SELECT 1 FROM course_access a WHERE a.course_id=c.id AND a.user_id=?) FROM courses c `

func courseScan(row scanner, u *User) (*Course, error) {
	c := &Course{}
	err := row.Scan(&c.ID, &c.Title, &c.Summary, &c.Topic, &c.PriceCents, &c.Status, &c.LessonCount, &c.Purchased)
	if err != nil {
		return nil, err
	}
	c.CanWatch = c.Purchased || courseAdmin(u)
	c.CanBuy = u != nil && !u.TestMode && !c.Purchased && c.Status == "published" && c.PriceCents > 0 && c.LessonCount > 0
	return c, nil
}
func courseAdmin(u *User) bool { return u != nil && u.Role == "admin" && !u.TestMode }
func courseUserID(u *User) int64 {
	if u != nil {
		return u.ID
	}
	return 0
}
func (s *server) courseRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/courses", s.listCourses)
	m.HandleFunc("GET /api/courses/{id}", s.getCourse)
	m.HandleFunc("GET /api/admin/courses", s.listAdminCourses)
	m.HandleFunc("POST /api/admin/courses", s.createCourse)
	m.HandleFunc("POST /api/admin/courses/{id}", s.editCourse)
	m.HandleFunc("POST /api/admin/courses/{id}/uploads", s.initCourseUploads)
	m.HandleFunc("PUT /api/admin/course-uploads/{upload}/parts/{index}", s.putCoursePart)
	m.HandleFunc("POST /api/admin/course-uploads/{upload}/complete", s.completeCourseUpload)
	m.HandleFunc("GET /api/courses/{id}/lessons/{lesson}/video", s.courseVideo)
}
func (s *server) listAdminCourses(w http.ResponseWriter, r *http.Request) {
	if s.staff(w, r, true) == nil {
		return
	}
	s.listCourseRows(w, r, true)
}
func (s *server) listCourses(w http.ResponseWriter, r *http.Request) { s.listCourseRows(w, r, false) }
func (s *server) listCourseRows(w http.ResponseWriter, r *http.Request, admin bool) {
	u := s.current(r)
	topic := r.URL.Query().Get("topic")
	if topic != "" && topic != "codex" {
		fail(w, 400, "未知课程主题")
		return
	}
	where := " WHERE c.status='published'"
	if admin {
		where = ""
	}
	rows, err := s.db.Query(courseSelect+where+" ORDER BY c.id DESC LIMIT 200", courseUserID(u))
	if err != nil {
		fail(w, 500, "课程列表读取失败")
		return
	}
	defer rows.Close()
	items := []*Course{}
	for rows.Next() {
		c, err := courseScan(rows, u)
		if err != nil {
			fail(w, 500, "课程列表读取失败")
			return
		}
		items = append(items, c)
	}
	if rows.Err() != nil {
		fail(w, 500, "课程列表读取失败")
		return
	}
	send(w, 200, map[string]any{"topics": []map[string]string{{"slug": "codex", "name": "Codex"}}, "items": items})
}
func (s *server) getCourse(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	c, err := courseScan(s.db.QueryRow(courseSelect+" WHERE c.id=?", courseUserID(u), idParam(r)), u)
	if err == sql.ErrNoRows || err == nil && c.Status != "published" && !c.CanWatch {
		fail(w, 404, "课程不存在或尚未上架")
		return
	}
	if err != nil {
		fail(w, 500, "课程读取失败")
		return
	}
	lessons, err := s.courseLessons(c.ID, u, c.CanWatch)
	if err != nil {
		fail(w, 500, "课时读取失败")
		return
	}
	send(w, 200, map[string]any{"course": c, "lessons": lessons})
}
func (s *server) courseLessons(id int64, u *User, watch bool) ([]CourseLesson, error) {
	q := `SELECT id,title,position,bytes,status FROM course_lessons WHERE course_id=?`
	if !courseAdmin(u) {
		q += ` AND status='ready'`
	}
	q += ` ORDER BY position,id`
	rows, err := s.db.Query(q, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []CourseLesson{}
	for rows.Next() {
		var l CourseLesson
		if err = rows.Scan(&l.ID, &l.Title, &l.Position, &l.Bytes, &l.Status); err != nil {
			return nil, err
		}
		if watch && l.Status == "ready" {
			l.MediaURL = fmt.Sprintf("/api/courses/%d/lessons/%d/video", id, l.ID)
		}
		items = append(items, l)
	}
	return items, rows.Err()
}

type courseInput struct {
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	Topic      string `json:"topic"`
	PriceCents int64  `json:"price_cents"`
	Status     string `json:"status"`
}

func validCourseInput(in *courseInput) bool {
	in.Title = strings.TrimSpace(in.Title)
	in.Summary = strings.TrimSpace(in.Summary)
	return utf8.RuneCountInString(in.Title) >= 2 && utf8.RuneCountInString(in.Title) <= 100 && utf8.RuneCountInString(in.Summary) <= 2000 && in.Topic == "codex" && in.PriceCents >= 0 && in.PriceCents <= 100000000 && (in.Status == "" || in.Status == "draft" || in.Status == "published")
}
func (s *server) createCourse(w http.ResponseWriter, r *http.Request) { s.saveCourse(w, r, true) }
func (s *server) editCourse(w http.ResponseWriter, r *http.Request)   { s.saveCourse(w, r, false) }
func (s *server) saveCourse(w http.ResponseWriter, r *http.Request, create bool) {
	u := s.staff(w, r, true)
	if u == nil {
		return
	}
	var in courseInput
	if !decode(w, r, &in) {
		return
	}
	if !validCourseInput(&in) {
		fail(w, 400, "请填写有效课程标题、简介、Codex 主题和价格")
		return
	}
	if in.Status == "" {
		in.Status = "draft"
	}
	if create && in.Status != "draft" {
		fail(w, 400, "新课程请先保存草稿并上传视频")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "课程保存失败")
		return
	}
	defer tx.Rollback()
	id := idParam(r)
	if create {
		res, e := tx.Exec(`INSERT INTO courses(title,summary,topic,price_cents,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, in.Title, in.Summary, in.Topic, in.PriceCents, u.ID, stamp(s.now()), stamp(s.now()))
		err = e
		if e == nil {
			id, err = res.LastInsertId()
		}
	} else {
		var exists int
		err = tx.QueryRow(`SELECT COUNT(*) FROM courses WHERE id=?`, id).Scan(&exists)
		if err == nil && exists == 0 {
			fail(w, 404, "课程不存在")
			return
		}
		if err == nil && in.Status == "published" {
			var ready int
			err = tx.QueryRow(`SELECT COUNT(*) FROM course_lessons WHERE course_id=? AND status='ready'`, id).Scan(&ready)
			if err == nil && (in.PriceCents <= 0 || ready == 0) {
				fail(w, 409, "设定价格并上传至少一个完整视频后才能上架")
				return
			}
		}
		if err == nil {
			_, err = tx.Exec(`UPDATE courses SET title=?,summary=?,topic=?,price_cents=?,status=?,updated_at=? WHERE id=?`, in.Title, in.Summary, in.Topic, in.PriceCents, in.Status, stamp(s.now()), id)
		}
	}
	if err == nil {
		err = audit(tx, r.Context(), u.ID, "course_save", strconv.FormatInt(id, 10), fmt.Sprintf("status=%s;price_cents=%d", in.Status, in.PriceCents), stamp(s.now()))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 500, "课程保存失败")
		return
	}
	c, err := courseScan(s.db.QueryRow(courseSelect+" WHERE c.id=?", u.ID, id), u)
	if err != nil {
		fail(w, 500, "课程读取失败")
		return
	}
	status := 200
	if create {
		status = 201
	}
	send(w, status, map[string]any{"course": c})
}
