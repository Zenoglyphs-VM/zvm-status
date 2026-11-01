package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIndependentObserverRejectsOutagesAndUntrustedFreshness(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, phase := range []string{"fresh", "stale", "host-death", "failed-prober", "future-clock", "long-window", "missing-schema", "false-ok", "notification-failure", "relay-unverified", "relay-accepted", "relay-old", "relay-reordered", "redirect", "html", "trailing-json", "oversized"} {
		t.Run(phase, func(t *testing.T) {
			r := monitoringReply{Schema: 1, OK: true, Monitoring: Monitoring{State: "fresh", CheckedAt: now.Add(-time.Minute).Unix(), FreshUntil: now.Add(10 * time.Minute).Unix(), Notifications: Notification{State: "unverified"}}}
			require := strings.HasPrefix(phase, "relay-")
			switch phase {
			case "stale":
				r.Monitoring.CheckedAt = now.Add(-time.Hour).Unix()
				r.Monitoring.FreshUntil = now.Add(-time.Minute).Unix()
			case "failed-prober":
				r.Monitoring.State = "failed"
			case "future-clock":
				r.Monitoring.CheckedAt = now.Add(time.Second).Unix()
			case "long-window":
				r.Monitoring.FreshUntil = now.Add(time.Hour).Unix()
			case "missing-schema":
				r.Schema = 0
			case "false-ok":
				r.OK = false
			case "notification-failure":
				r.Monitoring.Notifications.State = "failed"
			case "relay-accepted", "relay-old", "relay-reordered":
				r.Monitoring.Notifications = Notification{State: "accepted", AttemptedAt: now.Add(-time.Minute).Unix(), AcceptedAt: now.Add(-30 * time.Second).Unix()}
			}
			if phase == "relay-old" {
				r.Monitoring.Notifications.AcceptedAt = now.Add(-time.Hour).Unix()
			}
			if phase == "relay-reordered" {
				r.Monitoring.Notifications.AttemptedAt = now.Unix()
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch phase {
				case "redirect":
					http.Redirect(w, req, "https://unused.fixture.invalid", 302)
					return
				case "html":
					w.Write([]byte("<html>operational</html>"))
					return
				case "oversized":
					w.Write([]byte(strings.Repeat(" ", 33<<10)))
					return
				}
				json.NewEncoder(w).Encode(r)
				if phase == "trailing-json" {
					w.Write([]byte("{}"))
				}
			}))
			t.Cleanup(srv.Close)
			if phase == "host-death" {
				srv.Close()
			}
			err := observeStatus(context.Background(), srv.URL, 11*time.Minute, require, func() time.Time { return now })
			want := phase == "fresh" || phase == "relay-accepted"
			if (err == nil) != want {
				t.Fatalf("observer decision differs: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), srv.URL) {
				t.Fatal("observer exposed its private endpoint")
			}
		})
	}
}

func TestObserverDeadlineAndActualCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if observeStatus(ctx, srv.URL, time.Minute, false, time.Now) == nil {
		t.Fatal("nonresponding endpoint looked healthy")
	}
	w, at := freshWatcher(t)
	w.now = time.Now
	w.lastAt = time.Now().Truncate(time.Second)
	w.last["rpc"] = Check{At: at, Status: OK}
	health := httptest.NewServer(w.API())
	defer health.Close()
	var output bytes.Buffer
	if err := observerCommand([]string{"-url", health.URL + "/healthz"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Recipient delivery") {
		t.Fatal("observer overstated relay/delivery evidence")
	}
	if observerCommand([]string{"-url", health.URL + "/healthz", "-require-relay-acceptance"}, &output) == nil {
		t.Fatal("unverified relay was accepted")
	}
	if observerCommand([]string{"-url", "http://user:secret@fixture.invalid"}, &output) == nil {
		t.Fatal("user information was accepted")
	}
}
