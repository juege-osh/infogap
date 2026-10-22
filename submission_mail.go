package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

var submissionNotificationMail = sendSubmissionNotification

func sendSubmissionNotification(ctx context.Context, email, subject, body string) error {
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
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@infogap.osh.lol>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", (&mail.Address{Name: "InfoGap 前沿信息站", Address: c.from}).String(), email, mime.QEncoding.Encode("UTF-8", subject), time.Now().Format(time.RFC1123Z), token(16), strings.ReplaceAll(body, "\n", "\r\n"))
	if _, err = writer.Write([]byte(message)); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	_ = client.Quit()
	return nil
}
