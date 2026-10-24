package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

const notificationTimeout = 30 * time.Second

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
	ctx, cancel := context.WithTimeout(context.Background(), notificationTimeout)
	defer cancel()
	return m.SendContext(ctx, subject, body)
}

func (m *Mailer) SendContext(ctx context.Context, subject, body string) (err error) {
	ctx, cancel := context.WithTimeout(ctx, notificationTimeout)
	defer cancel()
	defer func() {
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			// The socket deadline can fire before the context timer runs.
			err = context.DeadlineExceeded
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
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
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
	}
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { rawConn.Close() })
	defer stop()
	if m.Port == "465" {
		secure := tls.Client(conn, &tls.Config{ServerName: m.Host})
		if err := secure.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = secure
	}
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	// Preserve the relay's STARTTLS negotiation while bounding greeting,
	// handshake, commands, body and final acceptance by the same context.
	if m.Port != "465" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: m.Host}); err != nil {
				return err
			}
		}
	}
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
