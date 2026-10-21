package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// An isolated browser fixture, compiled only by go test. Never linked into the
// production server. Its data lives in t.TempDir and its listener is loopback.
func TestBrowserPreview(t *testing.T) {
	if os.Getenv("INFOGAP_BROWSER_QA") != "1" {
		t.Skip("manual isolated browser QA")
	}
	s, err := newServer(t.TempDir()+"/qa.sqlite", "web", "http://127.0.0.1:18870")
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	now := stamp(time.Now())
	for _, u := range []struct {
		email, name, role string
		points            int
	}{{founderEmail, "站长预览", "admin", 0}, {"member@example.test", "科研成员", "member", 0}, {"reviewer@example.test", "核验成员", "reviewer", 0}, {"gold@example.test", "黄金研究员", "member", 700}, {"master@example.test", "大师研究员", "member", 6500}} {
		_, err = s.db.Exec("INSERT INTO users(email,username,role,points,created_at) VALUES(?,?,?,?,?)", u.email, u.name, u.role, u.points, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile("data/seed.json")
	if err != nil {
		t.Fatal(err)
	}
	var seeds []map[string]interface{}
	if err = json.Unmarshal(raw, &seeds); err != nil {
		t.Fatal(err)
	}
	for _, g := range seeds {
		tags, _ := json.Marshal(g["tags"])
		_, err = s.db.Exec("INSERT INTO gaps(title,summary,body,category,level,evidence,source_name,source_url,published_at,released_at,created_at,updated_at,status,tags) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'published',?)", g["title"], g["summary"], g["body"], g["category"], g["level"], g["evidence"], g["source_name"], g["source_url"], g["published_at"], g["published_at"], now, now, string(tags))
		if err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__qa/{role}", func(w http.ResponseWriter, r *http.Request) {
		var id int64
		email := map[string]string{"admin": founderEmail, "member": "member@example.test", "reviewer": "reviewer@example.test", "gold": "gold@example.test", "master": "master@example.test"}[r.PathValue("role")]
		if err := s.db.QueryRow("SELECT id FROM users WHERE email=?", email).Scan(&id); err != nil {
			http.NotFound(w, r)
			return
		}
		tok := token(32)
		_, err := s.db.Exec("INSERT INTO sessions(token_hash,user_id,created_at,expires_at) VALUES(?,?,?,?)", digest(tok), id, now, stamp(time.Now().Add(time.Hour)))
		if err != nil {
			http.Error(w, "fixture failure", 500)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "infogap_session", Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.Handle("/", s.routes())
	ln, err := net.Listen("tcp", "127.0.0.1:18870")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	t.Log("Isolated QA ready at http://127.0.0.1:18870")
	<-time.After(20 * time.Minute)
	srv.Shutdown(context.Background())
}
