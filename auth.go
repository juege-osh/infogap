package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

type authInput struct {
	Email    string `json:"email"`
	Invite   string `json:"invite"`
	Code     string `json:"code"`
	Username string `json:"username"`
}

func validEmail(v string) bool {
	a, e := mail.ParseAddress(v)
	return e == nil && a.Address == v && len(v) <= 254 && !strings.ContainsAny(v, "\r\n")
}
func (s *server) authCode(w http.ResponseWriter, r *http.Request) {
	var in authInput
	if !decode(w, r, &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Invite = strings.TrimSpace(in.Invite)
	if !validEmail(in.Email) {
		fail(w, 400, "请输入有效邮箱")
		return
	}
	var id, disabled int
	err := s.db.QueryRow("SELECT id,disabled FROM users WHERE email=?", in.Email).Scan(&id, &disabled)
	if err != nil && err != sql.ErrNoRows {
		fail(w, 500, "暂时无法发送验证码")
		return
	}
	if id != 0 {
		var testAccount int
		if e := s.db.QueryRow("SELECT COUNT(*) FROM test_accounts WHERE user_id=?", id).Scan(&testAccount); e != nil || testAccount > 0 {
			fail(w, 403, "测试账号仅能由站长切换进入")
			return
		}
	}
	if disabled != 0 {
		fail(w, 403, "账号已停用")
		return
	}
	if id == 0 && in.Email != founderEmail {
		var n int
		_ = s.db.QueryRow("SELECT COUNT(*) FROM invites WHERE code=? AND active=1 AND uses<max_uses AND expires_at>?", in.Invite, stamp(s.now())).Scan(&n)
		if n == 0 {
			fail(w, 403, "首次加入需要有效邀请码")
			return
		}
	}
	now := s.now()
	ip := remoteIP(r)
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "发送失败")
		return
	}
	defer tx.Rollback()
	var recent, hour, global int
	_ = tx.QueryRow("SELECT COUNT(*) FROM code_requests WHERE email=? AND created_at>?", in.Email, stamp(now.Add(-time.Minute))).Scan(&recent)
	_ = tx.QueryRow("SELECT COUNT(*) FROM code_requests WHERE ip=? AND created_at>?", ip, stamp(now.Add(-time.Hour))).Scan(&hour)
	_ = tx.QueryRow("SELECT COUNT(*) FROM code_requests WHERE created_at>?", stamp(now.Add(-time.Hour))).Scan(&global)
	if recent > 0 || hour >= 10 || global >= 100 {
		fail(w, 429, "验证码发送较频繁，请稍后再试")
		return
	}
	n, e := rand.Int(rand.Reader, big.NewInt(1000000))
	if e != nil {
		fail(w, 500, "发送失败")
		return
	}
	code := fmt.Sprintf("%06d", n.Int64())
	_, err = tx.Exec("INSERT INTO code_requests(email,ip,created_at) VALUES(?,?,?)", in.Email, ip, stamp(now))
	if err == nil {
		_, err = tx.Exec("INSERT INTO email_codes(email,code_hash,created_at,expires_at,attempts,ip) VALUES(?,?,?,?,0,?) ON CONFLICT(email) DO UPDATE SET code_hash=excluded.code_hash,created_at=excluded.created_at,expires_at=excluded.expires_at,attempts=0,ip=excluded.ip", in.Email, digest(s.otpSecret+in.Email+code), stamp(now), stamp(now.Add(10*time.Minute)), ip)
	}
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "发送失败")
		return
	}
	if err = s.mailer(r.Context(), in.Email, code); err != nil {
		_, _ = s.db.Exec("DELETE FROM email_codes WHERE email=? AND code_hash=?", in.Email, digest(s.otpSecret+in.Email+code))
		fail(w, 503, "邮件暂时发送失败，请稍后重试或联系站长")
		return
	}
	_, _ = s.db.Exec("DELETE FROM code_requests WHERE created_at<?", stamp(now.Add(-48*time.Hour)))
	send(w, 200, map[string]bool{"ok": true})
}
func (s *server) authVerify(w http.ResponseWriter, r *http.Request) {
	var in authInput
	if !decode(w, r, &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Username = strings.TrimSpace(in.Username)
	in.Invite = strings.TrimSpace(in.Invite)
	if !validEmail(in.Email) || len(in.Code) != 6 {
		fail(w, 400, "邮箱或验证码格式不正确")
		return
	}
	now := s.now()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "登录失败")
		return
	}
	defer tx.Rollback()
	var hash, expires string
	var attempts int
	err = tx.QueryRow("SELECT code_hash,expires_at,attempts FROM email_codes WHERE email=?", in.Email).Scan(&hash, &expires, &attempts)
	if err != nil || expires <= stamp(now) || attempts >= 5 {
		fail(w, 400, "验证码无效或已过期，请重新获取")
		return
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(digest(s.otpSecret+in.Email+in.Code))) != 1 {
		_, _ = tx.Exec("UPDATE email_codes SET attempts=attempts+1 WHERE email=?", in.Email)
		_ = tx.Commit()
		fail(w, 400, "验证码错误，请检查后重试")
		return
	}
	u, err := userScan(tx.QueryRow("SELECT "+userCols+" FROM users WHERE email=?", in.Email))
	if err == sql.ErrNoRows {
		// The verified founder can initialize the site from the login form,
		// which intentionally has no username or invite fields.
		if in.Email == founderEmail && in.Username == "" {
			in.Username = "觉哥"
			var used int
			if e := tx.QueryRow("SELECT COUNT(*) FROM users WHERE username=?", in.Username).Scan(&used); e != nil {
				fail(w, 500, "登录失败")
				return
			}
			if used > 0 {
				in.Username = "站长" + token(4)
			}
		}
		if in.Username == "" {
			fail(w, 400, "此邮箱尚未注册，请切换“受邀加入”并填写用户名和邀请码")
			return
		}
		if utf8.RuneCountInString(in.Username) < 2 || utf8.RuneCountInString(in.Username) > 24 || strings.ContainsAny(in.Username, "\r\n\t<>\x00") {
			fail(w, 400, "用户名需为 2–24 个字符")
			return
		}
		if in.Email != founderEmail {
			res, e := tx.Exec("UPDATE invites SET uses=uses+1 WHERE code=? AND active=1 AND uses<max_uses AND expires_at>?", in.Invite, stamp(now))
			if e != nil {
				fail(w, 500, "加入失败")
				return
			}
			n, _ := res.RowsAffected()
			if n != 1 {
				fail(w, 403, "邀请码无效、已用完或已过期")
				return
			}
		}
		role := "member"
		if in.Email == founderEmail {
			role = "admin"
		}
		res, e := tx.Exec("INSERT INTO users(email,username,role,created_at) VALUES(?,?,?,?)", in.Email, in.Username, role, stamp(now))
		if e != nil {
			fail(w, 409, "用户名已被使用，请换一个")
			return
		}
		uid, _ := res.LastInsertId()
		_, err = tx.Exec("INSERT INTO ledger(user_id,kind,amount,created_at,note) VALUES(?,'welcome',10,?,'新人体验额度')", uid, stamp(now))
		if err != nil {
			fail(w, 500, "加入失败")
			return
		}
		u, err = userScan(tx.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", uid))
	}
	if err != nil {
		fail(w, 500, "登录失败")
		return
	}
	var testAccount int
	if e := tx.QueryRow("SELECT COUNT(*) FROM test_accounts WHERE user_id=?", u.ID).Scan(&testAccount); e != nil || testAccount > 0 {
		fail(w, 403, "测试账号仅能由站长切换进入")
		return
	}
	if u.Disabled {
		fail(w, 403, "账号已停用")
		return
	}
	session := token(32)
	_, err = tx.Exec("INSERT INTO sessions(token_hash,user_id,created_at,expires_at) VALUES(?,?,?,?)", digest(session), u.ID, stamp(now), stamp(now.Add(30*24*time.Hour)))
	if err == nil {
		_, err = tx.Exec("DELETE FROM email_codes WHERE email=?", in.Email)
	}
	if err == nil {
		_, err = tx.Exec("DELETE FROM sessions WHERE expires_at<?", stamp(now))
	}
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "登录失败")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "infogap_session", Value: session, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600})
	send(w, 200, map[string]any{"user": s.decorate(u)})
}
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("infogap_session"); e == nil {
		_, _ = s.db.Exec("DELETE FROM sessions WHERE token_hash=?", digest(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "infogap_session", Value: "", Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	send(w, 200, map[string]bool{"ok": true})
}

type smtpConfig struct{ host, port, user, password, from, security string }

func smtpSettings() (smtpConfig, error) {
	c := smtpConfig{host: os.Getenv("SMTP_HOST"), port: os.Getenv("SMTP_PORT"), user: os.Getenv("SMTP_USER"), password: os.Getenv("SMTP_PASSWORD"), from: os.Getenv("SMTP_FROM"), security: os.Getenv("SMTP_SECURITY")}
	if c.port == "" {
		c.port = "465"
	}
	if c.from == "" {
		c.from = c.user
	}
	if c.security == "" {
		c.security = "starttls"
		if c.port == "465" {
			c.security = "tls"
		}
	}
	if c.host == "" || c.user == "" || c.password == "" || !validEmail(c.from) || (c.security != "tls" && c.security != "starttls") {
		return c, errors.New("SMTP not configured")
	}
	return c, nil
}
func sendCodeMail(ctx context.Context, email, code string) error {
	c, err := smtpSettings()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	d := &net.Dialer{Timeout: 10 * time.Second}
	tc := &tls.Config{ServerName: c.host, MinVersion: tls.VersionTLS12}
	address := net.JoinHostPort(c.host, c.port)
	var conn net.Conn
	if c.security == "tls" {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tc}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = d.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, c.host)
	if err != nil {
		return err
	}
	defer client.Close()
	if c.security == "starttls" {
		if err = client.StartTLS(tc); err != nil {
			return err
		}
	}
	if err = client.Auth(smtp.PlainAuth("", c.user, c.password, c.host)); err != nil {
		return err
	}
	if err = client.Mail(c.from); err != nil {
		return err
	}
	if err = client.Rcpt(email); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@infogap.osh.lol>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n你的 InfoGap 登录验证码：%s\r\n\r\n验证码 10 分钟内有效，仅可使用一次。请勿向他人提供。\r\n如果不是你本人操作，请忽略此邮件。\r\n\r\nInfoGap · 前沿信息站\r\nhttps://infogap.osh.lol\r\n", (&mail.Address{Name: "InfoGap 前沿信息站", Address: c.from}).String(), email, mime.QEncoding.Encode("UTF-8", "InfoGap 登录验证码"), time.Now().Format(time.RFC1123Z), token(16), code)
	if _, err = writer.Write([]byte(message)); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	_ = client.Quit()
	return nil
}
