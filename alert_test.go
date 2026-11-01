package main

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// This SMTP sink never relays messages. All addresses are reserved fixtures,
// the listener is loopback-only, and every accepted connection is joined.
func smtpFixture(t *testing.T, phase string) (*Mailer, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop, received, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var active net.Conn
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		active = conn
		mu.Unlock()
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if phase == "greeting-hang" {
			<-stop
			return
		}
		c := textproto.NewConn(conn)
		defer c.Close()
		if phase == "refuse" {
			c.PrintfLine("550 fixture refused")
			return
		}
		c.PrintfLine("220 fixture SMTP")
		for {
			line, err := c.ReadLine()
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO "), strings.HasPrefix(line, "HELO "):
				c.PrintfLine("250 fixture")
			case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
				c.PrintfLine("250 accepted")
			case line == "DATA":
				c.PrintfLine("354 fixture body")
				if _, err := c.ReadDotBytes(); err != nil {
					return
				}
				close(received)
				if phase == "acceptance-hang" {
					<-stop
					return
				}
				c.PrintfLine("250 queued by fixture")
			case line == "QUIT":
				c.PrintfLine("221 fixture closed")
				return
			default:
				c.PrintfLine("500 unsupported fixture operation")
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		ln.Close()
		mu.Lock()
		if active != nil {
			active.Close()
		}
		mu.Unlock()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("owned SMTP fixture did not join")
		}
	})
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return &Mailer{Host: host, Port: port, From: "sender@fixture.invalid", To: []string{"receiver@fixture.invalid"}}, received
}

func TestSMTPContextBoundsGreetingAndFinalAcceptance(t *testing.T) {
	for _, phase := range []string{"greeting-hang", "acceptance-hang"} {
		t.Run(phase, func(t *testing.T) {
			m, _ := smtpFixture(t, phase)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if err := m.SendContext(ctx, "fixture", "fixture body"); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("hung SMTP did not honor its shared deadline: %v", err)
			}
		})
	}
}

func TestRelayResultIsVisibleAndNoOpCannotClearFailure(t *testing.T) {
	w, at := freshWatcher(t)
	m, _ := smtpFixture(t, "refuse")
	w.mailer = m
	w.email("fixture", "fixture body")
	deadline := time.Now().Add(3 * time.Second)
	for w.monitoringAt(at).Notifications.State != "failed" {
		if time.Now().After(deadline) {
			t.Fatal("relay refusal was not observable")
		}
		time.Sleep(time.Millisecond)
	}
	first := w.etag(at)
	w.notify(at, nil, nil)
	if got := w.monitoringAt(at); got.State != "failed" || got.Failure != "notification_failed" {
		t.Fatalf("no-op round cleared failed delivery: %+v", got)
	}
	endpoint := httptest.NewServer(w.API())
	defer endpoint.Close()
	if observeStatus(context.Background(), endpoint.URL+"/healthz", 11*time.Minute, false, func() time.Time { return at }) == nil {
		t.Fatal("independent HTTP observer accepted the actual failed relay path")
	}
	m, _ = smtpFixture(t, "accepted")
	w.mailer = m
	w.email("fixture", "fixture body")
	for w.monitoringAt(at).Notifications.State != "accepted" {
		if time.Now().After(deadline) {
			t.Fatal("relay acceptance was not observable")
		}
		time.Sleep(time.Millisecond)
	}
	if w.monitoringAt(at).State != "fresh" || w.etag(at) == first {
		t.Fatal("actual new relay acceptance did not recover health and its validator")
	}
	if err := observeStatus(context.Background(), endpoint.URL+"/healthz", 11*time.Minute, true, func() time.Time { return at }); err != nil {
		t.Fatalf("independent HTTP observer did not see actual new relay acceptance: %v", err)
	}
}

func TestOverlappingRelayAttemptDoesNotSpawnAnotherSender(t *testing.T) {
	w, at := freshWatcher(t)
	m, received := smtpFixture(t, "acceptance-hang")
	w.mailer = m
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.runContext = ctx
	w.email("fixture one", "fixture body")
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("owned relay did not receive its first fixture body")
	}
	w.email("fixture two", "fixture body")
	if w.monitoringAt(at).Notifications.State != "failed" {
		t.Fatal("overlapping notification was hidden")
	}
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for {
		w.mu.RLock()
		running := w.mail.inFlight
		w.mu.RUnlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled relay did not stop")
		}
		time.Sleep(time.Millisecond)
	}
}
