package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// serve answers /status with whatever body() returns at the time of the call.
func serve(t *testing.T, code int, body func() string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		w.Write([]byte(body()))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func component(p *Prober, id string) *Component {
	for _, c := range p.Components() {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func TestRelayerBacklogThatStopsDrainingIsDegraded(t *testing.T) {
	srv := serve(t, 200, func() string { return `{"queued":5,"posted":100,"publishErrors":0}` })
	p := NewProber("http://unused", "", "", srv.URL, "", time.Second)
	c := component(p, "relayer")
	c.stallFor = 30 * time.Millisecond

	if r := p.Check(context.Background(), c); r.Status != OK {
		t.Fatalf("first sight of a backlog: got %s (%s), want operational", r.Status, r.Detail)
	}
	time.Sleep(50 * time.Millisecond)
	r := p.Check(context.Background(), c)
	if r.Status != Degraded || !strings.Contains(r.Detail, "5 queued, nothing posted") {
		t.Fatalf("got %s (%s), want degraded with the backlog named", r.Status, r.Detail)
	}
}

func TestRelayerIdleIsNotStuck(t *testing.T) {
	srv := serve(t, 200, func() string { return `{"queued":0,"posted":100,"publishErrors":0}` })
	p := NewProber("http://unused", "", "", srv.URL, "", time.Second)
	c := component(p, "relayer")
	c.stallFor = 10 * time.Millisecond

	p.Check(context.Background(), c)
	time.Sleep(30 * time.Millisecond)
	if r := p.Check(context.Background(), c); r.Status != OK {
		t.Fatalf("nothing queued and nothing posted is idle: got %s (%s)", r.Status, r.Detail)
	}
}

func TestRelayerNewPublishErrorsAreDegraded(t *testing.T) {
	var errs atomic.Int64
	errs.Store(3) // errors from before the prober started are not news
	srv := serve(t, 200, func() string {
		return `{"queued":0,"posted":100,"publishErrors":` + itoa(errs.Load()) + `}`
	})
	p := NewProber("http://unused", "", "", srv.URL, "", time.Second)
	c := component(p, "relayer")

	if r := p.Check(context.Background(), c); r.Status != OK {
		t.Fatalf("errors older than the first check: got %s (%s)", r.Status, r.Detail)
	}
	errs.Store(5)
	if r := p.Check(context.Background(), c); r.Status != Degraded || !strings.Contains(r.Detail, "2 publish errors") {
		t.Fatalf("got %s (%s), want degraded naming 2 new errors", r.Status, r.Detail)
	}
	if r := p.Check(context.Background(), c); r.Status != OK {
		t.Fatalf("no new errors since: got %s (%s)", r.Status, r.Detail)
	}
}

func TestBridgeGrading(t *testing.T) {
	recent := time.Now().UTC().Format(time.RFC3339)
	stale := time.Now().Add(-25 * time.Minute).UTC().Format(time.RFC3339)
	cases := []struct {
		name   string
		code   int
		body   string
		want   Status
		detail string
	}{
		{"healthy", 200, completeBridgeReport(time.Now().UTC()), OK, "6 paid"},
		{"last sweep failed", 200, `{"healthy":false,"lastOkAt":"` + recent + `","lastError":"receive: plasma"}`, Degraded, "receive: plasma"},
		{"loop stopped", 200, `{"healthy":true,"lastOkAt":"` + stale + `","lastError":"sign: no quorum"}`, Down, "no successful sweep for 25m0s: sign: no quorum"},
		{"cannot reach its chains", 502, `dial tcp: connection refused`, Down, "HTTP 502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(t, tc.code, func() string { return tc.body })
			p := NewProber("http://unused", "", "", "", srv.URL, time.Second)
			r := p.Check(context.Background(), component(p, "bridge"))
			if r.Status != tc.want || !strings.Contains(r.Detail, tc.detail) {
				t.Fatalf("got %s (%s), want %s containing %q", r.Status, r.Detail, tc.want, tc.detail)
			}
		})
	}
}

func itoa(n int64) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// The bots' pages are checked only when named: a stack without a bot token
// does not serve them, and two rows down every round is noise.
func TestBotsAreOptIn(t *testing.T) {
	p := NewProber("http://unused", "", "", "", "", time.Second)
	if component(p, "rain") != nil || component(p, "bullet") != nil {
		t.Fatal("a bot page is checked without being named")
	}
	p.WithBots(" Rain ")
	if component(p, "rain") == nil || component(p, "bullet") != nil {
		t.Fatal("WithBots(rain) did not check rain alone")
	}
}

// Rain's page has no #app; it is recognised by its canvas, and a page without
// that marker, or whose entry script is missing, is down.
func TestRainPage(t *testing.T) {
	page := `<html><canvas id="rain"></canvas><script type="module" crossorigin src="/rain/assets/index.js"></script></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rain/":
			w.Write([]byte(page))
		case "/rain/assets/index.js":
			w.Write([]byte("export {}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	p := NewProber(srv.URL, "", "", "", "", time.Second).WithBots("rain, bullet")
	c := component(p, "rain")
	if c == nil {
		t.Fatal("no rain component")
	}
	if r := p.Check(context.Background(), c); r.Status != OK {
		t.Fatalf("got %s (%s), want operational", r.Status, r.Detail)
	}
	page = `<html><div id="app"></div><script type="module" src="/rain/assets/index.js"></script></html>`
	if r := p.Check(context.Background(), c); r.Status == OK {
		t.Fatal("a page without the rain canvas passed")
	}
	if component(p, "zvm-rpc").probe == nil || component(p, "nom-http").probe == nil {
		t.Fatal("the head-watching probes lost their component")
	}
}

// Bullet's page is static HTML too, with the theme script before the module
// one; it is recognised by its main element, not by the first script it has.
func TestBulletPage(t *testing.T) {
	page := `<html><head><script>try{}catch(e){}</script><script type="module" crossorigin src="/bullet/assets/index.js"></script></head><body><main class="wrap" id="bullet"></main></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bullet/":
			w.Write([]byte(page))
		case "/bullet/assets/index.js":
			w.Write([]byte("export {}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	p := NewProber(srv.URL, "", "", "", "", time.Second).WithBots("bullet")
	c := component(p, "bullet")
	if c == nil {
		t.Fatal("no bullet component")
	}
	if r := p.Check(context.Background(), c); r.Status != OK {
		t.Fatalf("got %s (%s), want operational", r.Status, r.Detail)
	}
	// What the gateway answered while the page's container was not running.
	page = `<html><body>502 Bad Gateway</body></html>`
	if r := p.Check(context.Background(), c); r.Status == OK {
		t.Fatal("a page without the bullet marker passed")
	}
}

// zchan's build names its entry script relative to the page, "./assets/…",
// which must be fetched under /zchan/, not from the root.
func TestZchanPageRelativeScript(t *testing.T) {
	page := `<html><div id="form-slot"></div><script type="module" crossorigin src="./assets/index.js"></script></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zchan/":
			w.Write([]byte(page))
		case "/zchan/assets/index.js":
			w.Write([]byte("export {}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	p := NewProber(srv.URL, "", "", "", "", time.Second)
	c := component(p, "zchan")
	if c == nil {
		t.Fatal("no zchan component")
	}
	if r := p.Check(context.Background(), c); r.Status != OK {
		t.Fatalf("got %s (%s), want operational", r.Status, r.Detail)
	}
	page = `<html><div id="form-slot"></div><script type="module" crossorigin src="./assets/gone.js"></script></html>`
	if r := p.Check(context.Background(), c); r.Status == OK {
		t.Fatal("a page whose entry script is missing passed")
	}
}
