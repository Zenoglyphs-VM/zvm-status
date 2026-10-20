package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func watcher(t *testing.T) *Watcher {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "status.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows will not delete an open file, so the temp dir outlives the test
	// unless the handle is closed first.
	t.Cleanup(func() { store.db.Close() })
	comps := []*Component{{ID: "rpc", Name: "RPC", Group: "ZVM"}}
	return &Watcher{store: store, comps: comps, every: 5 * time.Minute, retain: 30 * 24 * time.Hour}
}

func get(t *testing.T, h http.Handler, path, inm string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if inm != "" {
		req.Header.Set("If-None-Match", inm)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A poll within the same round is a 304; the round after it is a new body.
func TestSummaryRevalidates(t *testing.T) {
	w := watcher(t)
	h := w.API()
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := w.store.Record("rpc", at, Result{Status: OK, Latency: 40 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	w.last, w.lastAt = map[string]Check{"rpc": {At: at, Status: OK, Latency: 40}}, at

	first := get(t, h, "/api/summary", "")
	if first.Code != 200 {
		t.Fatalf("first: %d", first.Code)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	var s Summary
	if err := json.Unmarshal(first.Body.Bytes(), &s); err != nil || len(s.Components) != 1 {
		t.Fatalf("summary: %v %+v", err, s)
	}

	// Cloudflare hands back a weakened tag, Caddy an encoding-suffixed one.
	for _, inm := range []string{etag, "W/" + etag, `W/"` + etag[1:len(etag)-1] + `-gzip"`} {
		if rec := get(t, h, "/api/summary", inm); rec.Code != http.StatusNotModified {
			t.Errorf("If-None-Match %s: %d, want 304", inm, rec.Code)
		}
	}

	next := at.Add(5 * time.Minute)
	w.store.Record("rpc", next, Result{Status: Down, Detail: "refused"})
	w.last, w.lastAt = map[string]Check{"rpc": {At: next, Status: Down, Detail: "refused"}}, next
	rec := get(t, h, "/api/summary", etag)
	if rec.Code != 200 {
		t.Fatalf("after a round: %d, want 200", rec.Code)
	}
	json.Unmarshal(rec.Body.Bytes(), &s)
	if s.Components[0].Status != "down" {
		t.Errorf("served a stale body: %s", s.Components[0].Status)
	}
}

func TestRecent(t *testing.T) {
	w := watcher(t)
	h := w.API()
	now := time.Now().Truncate(time.Second)
	w.store.Record("rpc", now.Add(-25*time.Hour), Result{Status: OK, Latency: time.Second})
	w.store.Record("rpc", now.Add(-time.Hour), Result{Status: OK, Latency: 50 * time.Millisecond})

	rec := get(t, h, "/api/recent/rpc", "")
	var pts []Point
	if err := json.Unmarshal(rec.Body.Bytes(), &pts); err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 || pts[0].Latency != 50 {
		t.Errorf("want the one check inside 24 h, got %+v", pts)
	}
	if rec := get(t, h, "/api/recent/nope", ""); rec.Code != 404 {
		t.Errorf("unknown component: %d, want 404", rec.Code)
	}
}
