package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Mailer sends alerts through an ordinary SMTP relay with a login.
//
// Not direct delivery to the recipient's MX: this machine's default route is a
// VPN, and residential and VPN exits are both blocked on port 25 by most
// providers and distrusted by the rest (Proton among them). A relay account
// (Proton's SMTP submission token, Fastmail, Postmark, SES, a Gmail app
// password) is the path that arrives.
type Mailer struct {
	Host, Port, User, Pass, From string
	To                           []string
}

func (m *Mailer) Configured() bool { return m != nil && m.Host != "" && len(m.To) > 0 }

func (m *Mailer) Send(subject, body string) error {
	if !m.Configured() {
		return fmt.Errorf("SMTP is not configured")
	}
	from := m.From
	if from == "" {
		from = m.User
	}
	msg := strings.Join([]string{
		"From: ZVM status <" + from + ">",
		"To: " + strings.Join(m.To, ", "),
		"Subject: " + subject,
		"Date: " + time.Now().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
	}, "\r\n")

	addr := net.JoinHostPort(m.Host, m.Port)
	var auth smtp.Auth
	if m.User != "" {
		auth = smtp.PlainAuth("", m.User, m.Pass, m.Host)
	}
	// 465 is TLS from the first byte; everything else (587, 25, 1025 for a
	// local catcher) starts plain, and smtp.SendMail upgrades with STARTTLS
	// when the server offers it.
	if m.Port != "465" {
		return smtp.SendMail(addr, auth, from, m.To, []byte(msg))
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 20 * time.Second}, "tcp", addr, &tls.Config{ServerName: m.Host})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, to := range m.To {
		if err := c.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
