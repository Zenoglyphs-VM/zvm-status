// zvm-status watches the public endpoints of a ZVM gateway, keeps 30 days
// of what it saw, emails when something goes down, and serves the history to
// the status page at /status.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	var (
		listen    = flag.String("listen", ":8090", "address the status API listens on")
		dbPath    = flag.String("db", "status.db", "SQLite file for the history")
		origin    = flag.String("origin", env("STATUS_ORIGIN", ""), "the gateway, as this process reaches it ($STATUS_ORIGIN)")
		host      = flag.String("host", env("STATUS_HOST", ""), "Host header to ask the gateway for; empty for the origin's own ($STATUS_HOST)")
		edge      = flag.String("edge", env("STATUS_EDGE", ""), "public URL, checked through the CDN; empty to skip ($STATUS_EDGE)")
		relayer   = flag.String("relayer", env("STATUS_RELAYER", ""), "the relayer, which is not public; empty to skip ($STATUS_RELAYER)")
		bridge    = flag.String("bridge", env("STATUS_BRIDGE", ""), "the bridge, which is not public; empty to skip ($STATUS_BRIDGE)")
		bots      = flag.String("bots", env("STATUS_BOTS", ""), "Telegram bots whose pages are checked, comma-separated (rain, bullet); empty for none ($STATUS_BOTS)")
		public    = flag.String("public", env("STATUS_PUBLIC", ""), "base URL named in alert emails ($STATUS_PUBLIC)")
		every     = flag.Duration("every", 5*time.Minute, "how often every component is checked")
		retain    = flag.Duration("retain", 30*24*time.Hour, "how long history is kept")
		slow      = flag.Duration("slow", 3*time.Second, "a check slower than this is degraded")
		retry     = flag.Duration("retry", 20*time.Second, "a failed check is tried once more after this, before it counts")
		once      = flag.Bool("once", false, "run one round, print it, and exit")
		testAlert = flag.Bool("test-alert", false, "send one test email and exit")
	)
	flag.Parse()
	if *origin == "" {
		log.Fatal("-origin is not set: pass it, or set STATUS_ORIGIN")
	}

	mailer := &Mailer{
		Host: env("SMTP_HOST", ""),
		Port: env("SMTP_PORT", "587"),
		User: env("SMTP_USER", ""),
		Pass: env("SMTP_PASS", ""),
		From: env("SMTP_FROM", ""),
	}
	for _, to := range strings.Split(env("ALERT_TO", ""), ",") {
		if to = strings.TrimSpace(to); to != "" {
			mailer.To = append(mailer.To, to)
		}
	}

	if *testAlert {
		err := mailer.Send("[ZVM status] test alert", "This is a test from zvm-status. If you can read it, down alerts will reach you.\n\n"+*public+"/status/\n")
		if err != nil {
			log.Fatalf("test alert: %v", err)
		}
		log.Printf("test alert sent to %s", strings.Join(mailer.To, ", "))
		return
	}

	prober := NewProber(*origin, *host, *edge, *relayer, *bridge, *slow).WithBots(*bots)
	comps := prober.Components()

	if *once {
		for _, c := range comps {
			r := prober.Check(context.Background(), c)
			fmt.Printf("%-16s %-12s %6dms  %s\n", c.ID, r.Status, r.Latency.Milliseconds(), r.Detail)
		}
		return
	}

	store, err := OpenStore(*dbPath)
	if err != nil {
		log.Fatalf("open %s: %v", *dbPath, err)
	}
	if !mailer.Configured() {
		log.Printf("SMTP_HOST is not set: outages are recorded but nobody is emailed")
	}

	w := &Watcher{
		prober: prober, store: store, mailer: mailer, comps: comps,
		every: *every, retain: *retain, retry: *retry, public: strings.TrimRight(*public, "/"),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go w.Run(ctx)

	srv := &http.Server{Addr: *listen, Handler: w.API(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Printf("checking %d components every %s; API on %s", len(comps), *every, *listen)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

type Watcher struct {
	prober *Prober
	store  *Store
	mailer *Mailer
	comps  []*Component
	every  time.Duration
	retain time.Duration
	retry  time.Duration
	public string

	mu     sync.RWMutex
	last   map[string]Check
	lastAt time.Time // when the last round started; zero until the first

	cache bodyCache
}

// Run checks once at start, then on the wall-clock grid (:00, :05, …), so that
// every component's checks land in the same slots and a day's bar is 288 of
// them however often the process restarts.
func (w *Watcher) Run(ctx context.Context) {
	w.round(ctx)
	for {
		now := time.Now()
		next := now.Truncate(w.every).Add(w.every)
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
		}
		w.round(ctx)
		if err := w.store.Prune(time.Now().Add(-w.retain)); err != nil {
			log.Printf("prune: %v", err)
		}
	}
}

func (w *Watcher) round(ctx context.Context) {
	at := time.Now().Truncate(time.Second)
	var wg sync.WaitGroup
	results := make([]Result, len(w.comps))
	for i, c := range w.comps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := w.prober.Check(ctx, c)
			// One retry before a failure counts: a single dropped connection
			// through two proxies is not an outage, and emailing about it
			// would teach the reader to ignore the email.
			if r.Status == Down && w.retry > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(w.retry):
				}
				r = w.prober.Check(ctx, c)
			}
			results[i] = r
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}

	last := map[string]Check{}
	var fell, rose []string
	for i, c := range w.comps {
		r := results[i]
		last[c.ID] = Check{At: at, Status: r.Status, Latency: r.Latency.Milliseconds(), Detail: r.Detail}
		if err := w.store.Record(c.ID, at, r); err != nil {
			log.Printf("record %s: %v", c.ID, err)
		}
		down, up, err := w.incident(c, at, r)
		if err != nil {
			log.Printf("incident %s: %v", c.ID, err)
		}
		if down != "" {
			fell = append(fell, down)
		}
		if up != "" {
			rose = append(rose, up)
		}
		if r.Status != OK {
			log.Printf("%s %s: %s", c.ID, r.Status, r.Detail)
		}
	}
	w.mu.Lock()
	w.last, w.lastAt = last, at
	w.mu.Unlock()
	w.notify(at, fell, rose)
}

// notify sends at most one email per round. When Docker stops, nine components
// fail at once, and nine emails about one event is how an alert address gets
// filtered into a folder nobody opens.
func (w *Watcher) notify(at time.Time, fell, rose []string) {
	if len(fell) == 0 && len(rose) == 0 {
		return
	}
	subject := ""
	var b strings.Builder
	if len(fell) > 0 {
		subject = "[ZVM status] DOWN: " + names(fell)
		b.WriteString("Down:\n\n")
		for _, line := range fell {
			b.WriteString("  " + line + "\n")
		}
		b.WriteString("\n")
	}
	if len(rose) > 0 {
		if subject == "" {
			subject = "[ZVM status] RECOVERED: " + names(rose)
		}
		b.WriteString("Recovered:\n\n")
		for _, line := range rose {
			b.WriteString("  " + line + "\n")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Checked %s. Every component is checked again in %s.\n\n%s/status/\n",
		at.UTC().Format(time.RFC1123), w.every, w.public)
	w.email(subject, b.String())
}

// names is the subject line's list: two at most, then a count, because a
// subject is read in a notification with about forty characters of room.
func names(lines []string) string {
	var out []string
	for _, l := range lines {
		out = append(out, strings.SplitN(l, " — ", 2)[0])
	}
	switch {
	case len(out) == 1:
		return out[0]
	case len(out) == 2:
		return out[0] + " and " + out[1]
	default:
		return fmt.Sprintf("%s, %s and %d more", out[0], out[1], len(out)-2)
	}
}

// incident opens, escalates and closes the episode for one component. It
// reports the two edges that are worth an email — into down, and back out of
// it — and leaves the sending to the round, which has the whole picture. A
// degraded component is shown on the page but emails nobody.
func (w *Watcher) incident(c *Component, at time.Time, r Result) (fell, rose string, err error) {
	open, err := w.store.Open(c.ID)
	if err != nil {
		return "", "", err
	}
	switch {
	case open == nil && r.Status == OK:
		return "", "", nil
	case open == nil:
		if open, err = w.store.OpenIncident(c.ID, at, r); err != nil {
			return "", "", err
		}
	case r.Status == OK:
		if err := w.store.Resolve(open.ID, at); err != nil {
			return "", "", err
		}
		if open.Alerted {
			rose = fmt.Sprintf("%s — down for %s, since %s",
				c.Name, at.Sub(open.Started).Round(time.Minute), open.Started.UTC().Format(time.RFC1123))
		}
		return "", rose, nil
	case r.Status > open.Worst:
		if err := w.store.Escalate(open.ID, r); err != nil {
			return "", "", err
		}
		open.Worst = r.Status
	}
	if r.Status == Down && !open.Alerted {
		// Marked before sending: a relay that is down too should not turn
		// every five-minute round into another attempt at the same email.
		if err := w.store.MarkAlerted(open.ID); err != nil {
			return "", "", err
		}
		fell = fmt.Sprintf("%s — %s (%s)", c.Name, r.Detail, c.Description)
	}
	return fell, "", nil
}

func (w *Watcher) email(subject, body string) {
	if !w.mailer.Configured() {
		log.Printf("alert not sent (no SMTP): %s", subject)
		return
	}
	go func() {
		if err := w.mailer.Send(subject, body); err != nil {
			log.Printf("alert %q: %v", subject, err)
			return
		}
		log.Printf("alert sent: %s", subject)
	}()
}
