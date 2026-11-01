package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func freshWatcher(t *testing.T) (*Watcher, time.Time) {
	w := watcher(t)
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	w.lastAt = at
	w.last = map[string]Check{"rpc": {At: at, Status: OK}}
	w.now = func() time.Time { return at }
	return w, at
}

func TestMonitoringHealthAndCacheExpireWithoutAnotherRound(t *testing.T) {
	w, at := freshWatcher(t)
	if err := w.store.Record("rpc", at, Result{Status: OK}); err != nil {
		t.Fatal(err)
	}
	now := at.Add(time.Minute)
	w.now = func() time.Time { return now }
	h := w.API()
	first := get(t, h, "/api/summary", "")
	if first.Code != 200 {
		t.Fatal(first.Code)
	}
	if get(t, h, "/healthz", "").Code != 200 {
		t.Fatal("fresh complete round was not healthy")
	}
	etag := first.Header().Get("ETag")
	if get(t, h, "/api/summary", etag).Code != 304 {
		t.Fatal("unchanged fresh response was not revalidated")
	}
	now = at.Add(11 * time.Minute)
	stale := get(t, h, "/api/summary", etag)
	if stale.Code != 200 || stale.Header().Get("ETag") == etag {
		t.Fatal("expiry did not invalidate the cached operational body")
	}
	var summary Summary
	if err := json.Unmarshal(stale.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Monitoring.State != "stale" || summary.Overall != "unknown" || summary.Components[0].Status != "operational" {
		t.Fatalf("staleness did not preserve historical observations: %+v", summary)
	}
	health := get(t, h, "/healthz", "")
	if health.Code != 503 || health.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("expired health could be cached as healthy")
	}
	w.lastAt = now
	w.last["rpc"] = Check{At: now, Status: OK}
	if get(t, h, "/healthz", "").Code != 200 {
		t.Fatal("new successful round did not recover")
	}
	if get(t, h, "/api/summary", stale.Header().Get("ETag")).Code != 200 {
		t.Fatal("new round retained an expired validator")
	}
}

func TestMonitoringRefusesUnestablishedOrFailedRounds(t *testing.T) {
	for _, phase := range []string{"starting", "future-clock", "history-failure", "incomplete", "stopped", "invalid-interval", "notification-failure", "pending-deadline"} {
		t.Run(phase, func(t *testing.T) {
			w, at := freshWatcher(t)
			switch phase {
			case "starting":
				w.lastAt = time.Time{}
			case "future-clock":
				w.lastAt = at.Add(time.Second)
			case "history-failure":
				w.roundFailed = true
			case "incomplete":
				w.last = nil
			case "stopped":
				w.stopped = true
			case "invalid-interval":
				w.every = 0
			case "notification-failure":
				w.mailer = &Mailer{Host: "fixture.invalid", To: []string{"receiver@fixture.invalid"}}
				w.mail.failed = true
			case "pending-deadline":
				w.mailer = &Mailer{Host: "fixture.invalid", To: []string{"receiver@fixture.invalid"}}
				w.mail.started = at.Add(-notificationTimeout)
				w.mail.inFlight = true
			}
			if got := get(t, w.API(), "/healthz", ""); got.Code != 503 {
				t.Fatalf("unestablished monitoring looked healthy: %s", got.Body.String())
			}
		})
	}
}

func TestMonitoringRunStopAndHistoryFailureAreVisible(t *testing.T) {
	w, at := freshWatcher(t)
	w.comps[0].probe = func(context.Context) (string, error) { return "fixture", nil }
	w.prober = NewProber("", "", "", "", "", time.Second)
	w.store.db.Close()
	w.round(context.Background())
	if got := w.monitoringAt(at); got.Failure != "history_write_failed" {
		t.Fatalf("write failure was hidden: %+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Run(ctx)
	if w.monitoringAt(at).State != "stopped" {
		t.Fatal("a stopped watcher looked active")
	}
}

func TestMonitoringHealthRejectsStoppedProbe(t *testing.T) {
	w := watcher(t)
	w.lastAt = time.Now().Add(-time.Hour)
	w.last = map[string]Check{"rpc": {At: w.lastAt, Status: OK}}
	rec := get(t, w.API(), "/healthz", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("stopped probe still looks healthy: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestExpiredMonitoringRejectsLegacyConditionalCopy(t *testing.T) {
	w := watcher(t)
	w.lastAt = time.Now().Add(-time.Hour).Truncate(time.Second)
	w.last = map[string]Check{"rpc": {At: w.lastAt, Status: OK}}
	if err := w.store.Record("rpc", w.lastAt, Result{Status: OK}); err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf(`"%d-%s"`, w.lastAt.Unix(), time.Now().UTC().Format("20060102"))
	rec := get(t, w.API(), "/api/summary", legacy)
	if rec.Code != http.StatusOK {
		t.Fatalf("expired monitoring reused the operational copy: status=%d etag=%s", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestMonotonicLeaseCannotBeExtendedByWallClockRollback(t *testing.T) {
	w, at := freshWatcher(t)
	w.leaseStarted = time.Now()
	w.leaseAge = func(time.Time) time.Duration { return 11 * time.Minute }
	if w.monitoringAt(at.Add(5*time.Minute)).State != "stale" {
		t.Fatal("wall clock extended the monotonic lease")
	}
}
