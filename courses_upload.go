package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const coursePartSize int64 = 8 << 20
const courseMaxFile int64 = 2 << 30

var courseUploadMu sync.Mutex
var courseKeyPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var courseBatchPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,100}$`)
var courseHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type courseUploadFile struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}
type courseUploadReceipt struct {
	ID        string `json:"id"`
	LessonID  int64  `json:"lesson_id"`
	Position  int    `json:"position"`
	PartSize  int64  `json:"part_size"`
	PartCount int    `json:"part_count"`
	Size      int64  `json:"size"`
	Completed bool   `json:"completed"`
}
type courseUpload struct {
	ID        string
	LessonID  int64
	CourseID  int64
	Size      int64
	PartCount int
	Status    string
	ExpiresAt string
	Position  int
	Title     string
	MediaKey  string
}

func courseEnvBytes(key string, def int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < 0 {
		return 0, errors.New("invalid course storage limit")
	}
	return n, nil
}
func (s *server) courseStorage() (string, error) {
	if strings.TrimSpace(s.courseDir) == "" {
		return "", errors.New("course storage unavailable")
	}
	root, e := filepath.Abs(s.courseDir)
	if e != nil {
		return "", e
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		return "", e
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return "", e
	}
	web, e := filepath.Abs(s.web)
	if e != nil {
		return "", e
	}
	if real, err := filepath.EvalSymlinks(web); err == nil {
		web = real
	}
	rel, e := filepath.Rel(web, root)
	if e != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
		return "", errors.New("course storage must be private")
	}
	for _, name := range []string{"uploads", "media"} {
		p := filepath.Join(root, name)
		if e = os.MkdirAll(p, 0700); e != nil {
			return "", e
		}
		st, e := os.Lstat(p)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe course storage")
		}
	}
	return root, nil
}
func courseFreeSpace(root string, need int64) error {
	reserve, e := courseEnvBytes("INFOGAP_COURSE_MIN_FREE_BYTES", 5<<30)
	if e != nil {
		return e
	}
	var st syscall.Statfs_t
	if e = syscall.Statfs(root, &st); e != nil {
		return e
	}
	free := uint64(st.Bavail) * uint64(st.Bsize)
	if uint64(need) > free || uint64(reserve) > free-uint64(need) {
		return errors.New("insufficient course storage")
	}
	return nil
}
func courseUploadDir(root, id string) (string, error) {
	if !courseKeyPattern.MatchString(id) {
		return "", errors.New("invalid upload")
	}
	p := filepath.Join(root, "uploads", id)
	if e := os.MkdirAll(p, 0700); e != nil {
		return "", e
	}
	st, e := os.Lstat(p)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("unsafe upload storage")
	}
	return p, nil
}
func (s *server) expireCourseUploads(root string) error {
	rows, e := s.db.Query(`SELECT id FROM course_uploads WHERE status='uploading' AND expires_at<=?`, stamp(s.now()))
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		if !courseKeyPattern.MatchString(id) {
			return errors.New("invalid stored upload")
		}
		if e = os.RemoveAll(filepath.Join(root, "uploads", id)); e != nil {
			return e
		}
		tx, e := s.db.Begin()
		if e != nil {
			return e
		}
		_, e = tx.Exec(`UPDATE course_lessons SET status='failed',updated_at=? WHERE id=(SELECT lesson_id FROM course_uploads WHERE id=?) AND status='uploading'`, stamp(s.now()), id)
		if e == nil {
			_, e = tx.Exec(`UPDATE course_uploads SET status='expired' WHERE id=? AND status='uploading'`, id)
		}
		if e == nil {
			_, e = tx.Exec(`DELETE FROM course_upload_parts WHERE upload_id=?`, id)
		}
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
		if e != nil {
			return e
		}
	}
	return nil
}
func (s *server) courseUploadReceipts(batch string) ([]courseUploadReceipt, error) {
	rows, e := s.db.Query(`SELECT u.id,l.id,l.position,u.part_count,u.expected_size,u.status FROM course_uploads u JOIN course_lessons l ON l.id=u.lesson_id WHERE u.batch_key=? ORDER BY l.position`, batch)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	items := []courseUploadReceipt{}
	for rows.Next() {
		v := courseUploadReceipt{PartSize: coursePartSize}
		var status string
		if e = rows.Scan(&v.ID, &v.LessonID, &v.Position, &v.PartCount, &v.Size, &status); e != nil {
			return nil, e
		}
		if status == "expired" {
			return nil, errors.New("upload batch expired")
		}
		v.Completed = status == "complete"
		items = append(items, v)
	}
	return items, rows.Err()
}
func (s *server) initCourseUploads(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, true)
	if u == nil {
		return
	}
	var in struct {
		BatchKey string             `json:"batch_key"`
		Files    []courseUploadFile `json:"files"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !courseBatchPattern.MatchString(in.BatchKey) || len(in.Files) < 1 || len(in.Files) > 30 {
		fail(w, 400, "每批请选择 1–30 个 MP4 视频")
		return
	}
	var total int64
	for i := range in.Files {
		f := &in.Files[i]
		f.Title = strings.TrimSpace(f.Title)
		f.Name = strings.TrimSpace(f.Name)
		if f.Size <= 0 || f.Size > courseMaxFile || utf8.RuneCountInString(f.Title) < 1 || utf8.RuneCountInString(f.Title) > 150 || utf8.RuneCountInString(f.Name) > 255 || !strings.EqualFold(filepath.Ext(f.Name), ".mp4") || strings.ContainsAny(f.Name, "/\\\x00\r\n") || f.ContentType != "video/mp4" {
			fail(w, 400, "仅支持每个不超过 2 GiB 的 MP4 视频，课时标题最多 150 字")
			return
		}
		total += f.Size
	}
	manifest, _ := json.Marshal(in.Files)
	hash := digest(string(manifest))
	courseUploadMu.Lock()
	defer courseUploadMu.Unlock()
	root, e := s.courseStorage()
	if e != nil {
		fail(w, 503, "视频存储暂不可用")
		return
	}
	if e = s.expireCourseUploads(root); e != nil {
		fail(w, 500, "上传记录清理失败")
		return
	}
	var prevHash string
	var prevCourse, prevActor int64
	e = s.db.QueryRow(`SELECT manifest_hash,course_id,actor_id FROM course_upload_batches WHERE batch_key=?`, in.BatchKey).Scan(&prevHash, &prevCourse, &prevActor)
	if e == nil {
		if prevHash != hash || prevCourse != idParam(r) || prevActor != u.ID {
			fail(w, 409, "该上传标识已用于另一批文件")
			return
		}
		items, e := s.courseUploadReceipts(in.BatchKey)
		if e != nil {
			fail(w, 409, "该上传批次已过期，请重新选择文件")
			return
		}
		send(w, 200, map[string]any{"uploads": items})
		return
	}
	if e != sql.ErrNoRows {
		fail(w, 500, "上传批次读取失败")
		return
	}
	quota, e := courseEnvBytes("INFOGAP_COURSE_MAX_BYTES", 80<<30)
	if e != nil {
		fail(w, 503, "视频存储暂不可用")
		return
	}
	var used int64
	e = s.db.QueryRow(`SELECT COALESCE(SUM(bytes),0) FROM course_lessons WHERE status IN ('uploading','ready')`).Scan(&used)
	if e != nil {
		fail(w, 500, "存储额度读取失败")
		return
	}
	if total > quota || used > quota-total || courseFreeSpace(root, total*2) != nil {
		fail(w, 507, "视频存储空间不足，请联系站长")
		return
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, "上传初始化失败")
		return
	}
	defer tx.Rollback()
	var exists int
	e = tx.QueryRow(`SELECT COUNT(*) FROM courses WHERE id=?`, idParam(r)).Scan(&exists)
	if e != nil {
		fail(w, 500, "课程读取失败")
		return
	}
	if exists == 0 {
		fail(w, 404, "课程不存在")
		return
	}
	var pos int
	e = tx.QueryRow(`SELECT COALESCE(MAX(position),0) FROM course_lessons WHERE course_id=?`, idParam(r)).Scan(&pos)
	if e == nil {
		_, e = tx.Exec(`INSERT INTO course_upload_batches(batch_key,course_id,actor_id,manifest_hash,created_at) VALUES(?,?,?,?,?)`, in.BatchKey, idParam(r), u.ID, hash, stamp(s.now()))
	}
	for i, f := range in.Files {
		if e != nil {
			break
		}
		var res sql.Result
		res, e = tx.Exec(`INSERT INTO course_lessons(course_id,title,position,bytes,created_at,updated_at) VALUES(?,?,?,?,?,?)`, idParam(r), f.Title, pos+i+1, f.Size, stamp(s.now()), stamp(s.now()))
		if e != nil {
			break
		}
		var lid int64
		lid, e = res.LastInsertId()
		if e != nil {
			break
		}
		_, e = tx.Exec(`INSERT INTO course_uploads(id,lesson_id,batch_key,expected_size,part_count,created_at,expires_at) VALUES(?,?,?,?,?,?,?)`, token(16), lid, in.BatchKey, f.Size, (f.Size+coursePartSize-1)/coursePartSize, stamp(s.now()), stamp(s.now().Add(24*time.Hour)))
	}
	if e == nil {
		e = audit(tx, r.Context(), u.ID, "course_upload_batch", strconv.FormatInt(idParam(r), 10), fmt.Sprintf("files=%d;bytes=%d", len(in.Files), total), stamp(s.now()))
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		fail(w, 500, "上传初始化失败")
		return
	}
	items, e := s.courseUploadReceipts(in.BatchKey)
	if e != nil {
		fail(w, 500, "上传批次读取失败")
		return
	}
	send(w, 201, map[string]any{"uploads": items})
}
func (s *server) loadCourseUpload(id string) (*courseUpload, error) {
	if !courseKeyPattern.MatchString(id) {
		return nil, sql.ErrNoRows
	}
	v := &courseUpload{}
	e := s.db.QueryRow(`SELECT u.id,u.lesson_id,l.course_id,u.expected_size,u.part_count,u.status,u.expires_at,l.position,l.title,l.media_key FROM course_uploads u JOIN course_lessons l ON l.id=u.lesson_id WHERE u.id=?`, id).Scan(&v.ID, &v.LessonID, &v.CourseID, &v.Size, &v.PartCount, &v.Status, &v.ExpiresAt, &v.Position, &v.Title, &v.MediaKey)
	return v, e
}
func (s *server) putCoursePart(w http.ResponseWriter, r *http.Request) {
	if s.staff(w, r, true) == nil {
		return
	}
	idx, e := strconv.Atoi(r.PathValue("index"))
	hash := r.Header.Get("X-Content-SHA256")
	if e != nil || idx < 0 || strconv.Itoa(idx) != r.PathValue("index") || !courseHashPattern.MatchString(hash) {
		fail(w, 400, "分片编号或校验值不正确")
		return
	}
	courseUploadMu.Lock()
	defer courseUploadMu.Unlock()
	v, e := s.loadCourseUpload(r.PathValue("upload"))
	if e == sql.ErrNoRows {
		fail(w, 404, "上传记录不存在")
		return
	}
	if e != nil {
		fail(w, 500, "上传记录读取失败")
		return
	}
	if v.Status != "uploading" || v.ExpiresAt <= stamp(s.now()) {
		fail(w, 409, "上传已结束或过期")
		return
	}
	if idx >= v.PartCount {
		fail(w, 400, "分片编号超出范围")
		return
	}
	expected := coursePartSize
	if idx == v.PartCount-1 {
		expected = v.Size - int64(idx)*coursePartSize
	}
	if r.ContentLength >= 0 && r.ContentLength != expected {
		fail(w, 400, "分片长度不正确")
		return
	}
	root, e := s.courseStorage()
	if e != nil {
		fail(w, 503, "视频存储暂不可用")
		return
	}
	dir, e := courseUploadDir(root, v.ID)
	if e != nil {
		fail(w, 500, "上传目录不可用")
		return
	}
	if e = courseFreeSpace(root, expected); e != nil {
		fail(w, 507, "视频存储空间不足")
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(120 * time.Second))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(130 * time.Second))
	f, e := os.CreateTemp(dir, "part-*.tmp")
	if e != nil {
		fail(w, 500, "分片写入失败")
		return
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	h := sha256.New()
	n, e := io.CopyBuffer(io.MultiWriter(f, h), http.MaxBytesReader(w, r.Body, expected+1), make([]byte, 64<<10))
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil || n != expected {
		fail(w, 400, "分片不完整，请重试")
		return
	}
	if hex.EncodeToString(h.Sum(nil)) != hash {
		fail(w, 400, "分片校验失败，请重试")
		return
	}
	var oldHash string
	var oldSize int64
	e = s.db.QueryRow(`SELECT sha256,bytes FROM course_upload_parts WHERE upload_id=? AND part_index=?`, v.ID, idx).Scan(&oldHash, &oldSize)
	if e == nil {
		if oldHash != hash || oldSize != n {
			fail(w, 409, "该分片已上传不同内容")
			return
		}
		// A verified same-content retry also repairs a missing part after a crash.
		if e = os.Rename(tmp, filepath.Join(dir, fmt.Sprintf("%d.part", idx))); e != nil {
			fail(w, 500, "分片保存失败")
			return
		}
		send(w, 200, map[string]any{"index": idx, "received": true, "sha256": hash})
		return
	}
	if e != sql.ErrNoRows {
		fail(w, 500, "分片记录读取失败")
		return
	}
	dest := filepath.Join(dir, fmt.Sprintf("%d.part", idx))
	if e = os.Rename(tmp, dest); e != nil {
		fail(w, 500, "分片保存失败")
		return
	}
	_, e = s.db.Exec(`INSERT INTO course_upload_parts(upload_id,part_index,bytes,sha256) VALUES(?,?,?,?)`, v.ID, idx, n, hash)
	if e != nil {
		os.Remove(dest)
		fail(w, 500, "分片记录保存失败")
		return
	}
	send(w, 200, map[string]any{"index": idx, "received": true, "sha256": hash})
}
func courseMP4Header(f *os.File, size int64) error {
	var offset int64
	ftyp, moov, mdat := false, false, false
	boxes := 0
	for offset < size {
		boxes++
		if boxes > 100000 {
			return errors.New("too many MP4 boxes")
		}
		if size-offset < 8 {
			return errors.New("invalid MP4 box")
		}
		var head [16]byte
		if _, e := f.ReadAt(head[:8], offset); e != nil {
			return e
		}
		n := int64(binary.BigEndian.Uint32(head[:4]))
		header := int64(8)
		if n == 1 {
			if size-offset < 16 {
				return errors.New("invalid MP4 box")
			}
			if _, e := f.ReadAt(head[8:16], offset+8); e != nil {
				return e
			}
			u := binary.BigEndian.Uint64(head[8:16])
			if u > uint64(size) {
				return errors.New("invalid MP4 box")
			}
			n = int64(u)
			header = 16
		}
		if n == 0 {
			n = size - offset
		}
		if n < header || n > size-offset {
			return errors.New("invalid MP4 size")
		}
		kind := string(head[4:8])
		switch kind {
		case "ftyp":
			if offset != 0 || n < header+8 {
				return errors.New("invalid MP4 header")
			}
			var brand [4]byte
			if _, e := f.ReadAt(brand[:], offset+header); e != nil {
				return e
			}
			if !strings.HasPrefix(string(brand[:]), "iso") && !strings.HasPrefix(string(brand[:]), "mp4") && string(brand[:]) != "avc1" && string(brand[:]) != "M4V " && string(brand[:]) != "dash" {
				return errors.New("unsupported MP4 brand")
			}
			ftyp = true
		case "moov":
			moov = n > header
		case "mdat":
			mdat = n > header
		}
		offset += n
	}
	if !ftyp || !moov || !mdat {
		return errors.New("MP4 requires ftyp, moov and mdat")
	}
	return nil
}
func (s *server) completeCourseUpload(w http.ResponseWriter, r *http.Request) {
	u := s.staff(w, r, true)
	if u == nil {
		return
	}
	var in struct {
		PartHashes []string `json:"part_hashes"`
	}
	if !decode(w, r, &in) {
		return
	}
	courseUploadMu.Lock()
	defer courseUploadMu.Unlock()
	v, e := s.loadCourseUpload(r.PathValue("upload"))
	if e == sql.ErrNoRows {
		fail(w, 404, "上传记录不存在")
		return
	}
	if e != nil {
		fail(w, 500, "上传记录读取失败")
		return
	}
	if len(in.PartHashes) != v.PartCount {
		fail(w, 400, "分片列表不完整")
		return
	}
	for _, h := range in.PartHashes {
		if !courseHashPattern.MatchString(h) {
			fail(w, 400, "分片校验值不正确")
			return
		}
	}
	rows, e := s.db.Query(`SELECT part_index,bytes,sha256 FROM course_upload_parts WHERE upload_id=? ORDER BY part_index`, v.ID)
	if e != nil {
		fail(w, 500, "分片读取失败")
		return
	}
	count := 0
	valid := true
	for rows.Next() {
		var idx int
		var n int64
		var h string
		if e = rows.Scan(&idx, &n, &h); e != nil {
			valid = false
			break
		}
		expected := coursePartSize
		if count == v.PartCount-1 {
			expected = v.Size - int64(count)*coursePartSize
		}
		if idx != count || idx >= len(in.PartHashes) || h != in.PartHashes[idx] || n != expected {
			valid = false
			break
		}
		count++
	}
	if rows.Err() != nil {
		valid = false
	}
	rows.Close()
	if !valid || count != v.PartCount {
		fail(w, 409, "分片未上传完整或校验列表不一致")
		return
	}
	lesson := CourseLesson{ID: v.LessonID, Title: v.Title, Position: v.Position, Bytes: v.Size, Status: "ready", MediaURL: fmt.Sprintf("/api/courses/%d/lessons/%d/video", v.CourseID, v.LessonID)}
	if v.Status == "complete" {
		send(w, 200, map[string]any{"lesson": lesson})
		return
	}
	if v.Status != "uploading" || v.ExpiresAt <= stamp(s.now()) {
		fail(w, 409, "上传已过期，请重新选择文件")
		return
	}
	root, e := s.courseStorage()
	if e != nil {
		fail(w, 503, "视频存储暂不可用")
		return
	}
	if courseFreeSpace(root, v.Size) != nil {
		fail(w, 507, "视频存储空间不足")
		return
	}
	dir, e := courseUploadDir(root, v.ID)
	if e != nil {
		fail(w, 500, "上传目录不可用")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Minute))
	f, e := os.CreateTemp(filepath.Join(root, "media"), "merge-*.tmp")
	if e != nil {
		fail(w, 500, "视频合并失败")
		return
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	fullHash := sha256.New()
	var total int64
	for i, h := range in.PartHashes {
		if r.Context().Err() != nil {
			fail(w, 408, "上传请求已中断，请重试")
			return
		}
		partPath := filepath.Join(dir, fmt.Sprintf("%d.part", i))
		st, err := os.Lstat(partPath)
		if err != nil || !st.Mode().IsRegular() {
			fail(w, 409, "分片文件缺失，请重新上传")
			return
		}
		p, err := os.Open(partPath)
		if err != nil {
			fail(w, 500, "分片文件读取失败")
			return
		}
		ph := sha256.New()
		n, err := io.CopyBuffer(io.MultiWriter(f, fullHash, ph), io.LimitReader(p, coursePartSize+1), make([]byte, 64<<10))
		p.Close()
		expected := coursePartSize
		if i == v.PartCount-1 {
			expected = v.Size - int64(i)*coursePartSize
		}
		if err != nil || n != expected || hex.EncodeToString(ph.Sum(nil)) != h {
			fail(w, 409, "分片文件校验失败，请重新上传")
			return
		}
		total += n
	}
	if total != v.Size || courseMP4Header(f, total) != nil {
		fail(w, 400, "视频不是完整 MP4 文件，请检查后重新上传")
		return
	}
	if e = f.Sync(); e != nil {
		fail(w, 500, "视频写入失败")
		return
	}
	if e = f.Close(); e != nil {
		fail(w, 500, "视频写入失败")
		return
	}
	key := token(16)
	dest := filepath.Join(root, "media", key+".mp4")
	if e = os.Rename(tmp, dest); e != nil {
		fail(w, 500, "视频保存失败")
		return
	}
	committed := false
	defer func() {
		if !committed {
			os.Remove(dest)
		}
	}()
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, "视频保存失败")
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec(`UPDATE course_lessons SET status='ready',media_key=?,sha256=?,updated_at=? WHERE id=? AND status='uploading'`, key, hex.EncodeToString(fullHash.Sum(nil)), stamp(s.now()), v.LessonID)
	if e == nil {
		_, e = tx.Exec(`UPDATE course_uploads SET status='complete' WHERE id=? AND status='uploading'`, v.ID)
	}
	if e == nil {
		e = audit(tx, r.Context(), u.ID, "course_video_ready", strconv.FormatInt(v.LessonID, 10), fmt.Sprintf("course=%d;position=%d;bytes=%d", v.CourseID, v.Position, total), stamp(s.now()))
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		fail(w, 500, "视频保存失败")
		return
	}
	committed = true
	_ = os.RemoveAll(dir)
	send(w, 200, map[string]any{"lesson": lesson})
}
func (s *server) courseVideo(w http.ResponseWriter, r *http.Request) {
	u := s.require(w, r)
	if u == nil {
		return
	}
	cid := idParam(r)
	lid, e := strconv.ParseInt(r.PathValue("lesson"), 10, 64)
	if e != nil || cid <= 0 || lid <= 0 {
		fail(w, 404, "视频不存在")
		return
	}
	var key, updated string
	var owned bool
	var size int64
	e = s.db.QueryRow(`SELECT l.media_key,l.bytes,l.updated_at,EXISTS(SELECT 1 FROM course_access WHERE course_id=l.course_id AND user_id=?) FROM course_lessons l WHERE l.course_id=? AND l.id=? AND l.status='ready'`, u.ID, cid, lid).Scan(&key, &size, &updated, &owned)
	if e == sql.ErrNoRows {
		fail(w, 404, "视频不存在")
		return
	}
	if e != nil {
		fail(w, 500, "视频读取失败")
		return
	}
	if !courseAdmin(u) && !owned {
		fail(w, 403, "请单独购买该课程后观看")
		return
	}
	root, e := s.courseStorage()
	if e != nil {
		fail(w, 503, "视频存储暂不可用")
		return
	}
	if !courseKeyPattern.MatchString(key) {
		fail(w, 503, "视频暂不可用")
		return
	}
	path := filepath.Join(root, "media", key+".mp4")
	st, e := os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() || st.Size() != size {
		fail(w, 503, "视频暂不可用")
		return
	}
	f, e := os.Open(path)
	if e != nil {
		fail(w, 503, "视频暂不可用")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	modified, _ := time.Parse(time.RFC3339, updated)
	http.ServeContent(courseMediaResponse{w}, r, "lesson.mp4", modified, f)
}

// ServeContent clears cache headers on 416 and other errors; restore no-store
// when it commits any authenticated media response, including an error response.
type courseMediaResponse struct{ http.ResponseWriter }

func (w courseMediaResponse) WriteHeader(code int) {
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.ResponseWriter.WriteHeader(code)
}
func (w courseMediaResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
