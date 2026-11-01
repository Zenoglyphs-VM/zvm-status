package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// What the page gets, in one request. Everything is computed here from raw
// rows so the page does no arithmetic that could disagree with the store.
//
// The 24-hour latency series is not in it. It was 83% of the summary (170 KB
// of 206 KB, eleven components times 288 points) and is drawn only for a row
// someone has opened, so it is fetched per component, on open, from
// /api/recent/{id}.

type Day struct {
	Date     string   `json:"date"` // UTC, YYYY-MM-DD
	Checks   int      `json:"checks"`
	Degraded int      `json:"degraded"`
	Down     int      `json:"down"`
	Uptime   *float64 `json:"uptime"` // null when there were no checks that day
	Worst    string   `json:"worst"`  // "none" when there were no checks
}

type Point struct {
	T       int64  `json:"t"`
	Latency int64  `json:"latency"`
	Status  string `json:"status"`
}

type ComponentView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Group       string   `json:"group"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Detail      string   `json:"detail"`
	Latency     int64    `json:"latency"`
	CheckedAt   int64    `json:"checkedAt"`
	Uptime      *float64 `json:"uptime"`
	Days        []Day    `json:"days"`
}

type IncidentView struct {
	ID        int64  `json:"id"`
	Component string `json:"component"`
	Name      string `json:"name"`
	Started   int64  `json:"started"`
	Resolved  *int64 `json:"resolved"`
	Worst     string `json:"worst"`
	Detail    string `json:"detail"`
}

type Summary struct {
	Generated  int64           `json:"generated"`
	Interval   int64           `json:"interval"` // seconds
	Days       int             `json:"days"`
	Overall    string          `json:"overall"`
	Alerts     bool            `json:"alerts"`
	Monitoring Monitoring      `json:"monitoring"`
	Components []ComponentView `json:"components"`
	Incidents  []IncidentView  `json:"incidents"`
}

// Both answers change only when a round lands or the UTC day turns over, and
// the page asks every few seconds around each round from every open tab. So a
// body is computed once per round and reused, and a tab that already has it is
// told so with a 304 instead of being sent it again. Before this, each poll
// re-read every check of the last 30 days: some 95,000 rows per tab per poll.
type bodyCache struct {
	mu      sync.Mutex
	entries map[string]cachedBody
}

type cachedBody struct {
	etag string
	body []byte
}

// etag names the data as of now: the last round this process ran, and the
// day, because the 30-day window slides at midnight UTC without a round.
func (w *Watcher) etag(now time.Time) string {
	w.mu.RLock()
	at := w.lastAt
	w.mu.RUnlock()
	round := int64(0)
	if !at.IsZero() {
		round = at.Unix()
	}
	monitor, _ := json.Marshal(w.monitoringAt(now))
	digest := sha256.Sum256(monitor)
	return fmt.Sprintf(`"%d-%s-%x"`, round, now.UTC().Format("20060102"), digest[:8])
}

// matches is If-None-Match's weak comparison. Cloudflare weakens an ETag
// when it compresses a response and Caddy's encoder suffixes one with the
// encoding, so the tag that comes back is rarely the tag that went out.
func matches(header, etag string) bool {
	want := strings.Trim(etag, `"`)
	for _, t := range strings.Split(header, ",") {
		t = strings.Trim(strings.TrimPrefix(strings.TrimSpace(t), "W/"), `"`)
		if t == "*" || t == want || strings.HasPrefix(t, want+"-") {
			return true
		}
	}
	return false
}

func (w *Watcher) serveCached(rw http.ResponseWriter, r *http.Request, name string, compute func(now time.Time) (any, error)) {
	now := w.timeNow()
	etag := w.etag(now)
	h := rw.Header()
	// no-cache, not no-store: the browser keeps the body and asks each time
	// whether it is still current, which is what makes the 304 possible.
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", etag)
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Expose-Headers", "ETag")
	if matches(r.Header.Get("If-None-Match"), etag) {
		rw.WriteHeader(http.StatusNotModified)
		return
	}

	w.cache.mu.Lock()
	c, ok := w.cache.entries[name]
	w.cache.mu.Unlock()
	if !ok || c.etag != etag {
		v, err := compute(now)
		if err != nil {
			log.Printf("%s: %v", name, err)
			http.Error(rw, "could not read the history", http.StatusInternalServerError)
			return
		}
		if v == nil {
			http.NotFound(rw, r)
			return
		}
		body, err := json.Marshal(v)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		c = cachedBody{etag: etag, body: body}
		w.cache.mu.Lock()
		if w.cache.entries == nil {
			w.cache.entries = map[string]cachedBody{}
		}
		w.cache.entries[name] = c
		w.cache.mu.Unlock()
	}
	h.Set("Content-Type", "application/json")
	rw.Write(c.body)
}

func (w *Watcher) API() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/summary", func(rw http.ResponseWriter, r *http.Request) {
		w.serveCached(rw, r, "summary", func(now time.Time) (any, error) { return w.summary(now) })
	})
	mux.HandleFunc("GET /api/recent/{id}", func(rw http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		w.serveCached(rw, r, "recent/"+id, func(now time.Time) (any, error) { return w.recent(id, now) })
	})
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Cache-Control", "no-store")
		monitor := w.monitoringAt(w.timeNow())
		ok := monitor.State == "fresh"
		if !ok {
			rw.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(rw).Encode(struct {
			Schema     int        `json:"schema"`
			OK         bool       `json:"ok"`
			Monitoring Monitoring `json:"monitoring"`
		}{1, ok, monitor})
	})
	return mux
}

// recent is one component's checks over the last 24 hours, for its latency
// chart. nil for a component that does not exist, which is a 404.
func (w *Watcher) recent(id string, now time.Time) (any, error) {
	known := false
	for _, c := range w.comps {
		known = known || c.ID == id
	}
	if !known {
		return nil, nil
	}
	checks, err := w.store.Since(id, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	out := make([]Point, 0, len(checks))
	for _, ch := range checks {
		out = append(out, Point{T: ch.At.Unix(), Latency: ch.Latency, Status: ch.Status.String()})
	}
	return out, nil
}

func (w *Watcher) summary(now time.Time) (*Summary, error) {
	days := int(w.retain / (24 * time.Hour))
	if days < 1 {
		days = 1
	}
	today := now.UTC().Truncate(24 * time.Hour)
	from := today.AddDate(0, 0, -(days - 1))

	w.mu.RLock()
	last := w.last
	w.mu.RUnlock()

	out := &Summary{
		Generated:  now.Unix(),
		Interval:   int64(w.every / time.Second),
		Days:       days,
		Alerts:     w.mailer.Configured(),
		Overall:    OK.String(),
		Monitoring: w.monitoringAt(now),
	}
	names := map[string]string{}
	worstNow := OK
	for _, c := range w.comps {
		names[c.ID] = c.Name
		checks, err := w.store.Since(c.ID, from)
		if err != nil {
			return nil, err
		}
		v := ComponentView{ID: c.ID, Name: c.Name, Group: c.Group, Description: c.Description, Status: "unknown"}

		// The latest observation: this process's last round, or the store's
		// last row if the process has only just started.
		cur, ok := last[c.ID]
		if !ok && len(checks) > 0 {
			cur, ok = checks[len(checks)-1], true
		}
		if ok {
			v.Status, v.Detail, v.Latency, v.CheckedAt = cur.Status.String(), cur.Detail, cur.Latency, cur.At.Unix()
			worstNow = max(worstNow, cur.Status)
		}

		byDay := make([]Day, days)
		for i := range byDay {
			byDay[i] = Day{Date: from.AddDate(0, 0, i).Format("2006-01-02"), Worst: "none"}
		}
		var total, up int
		for _, ch := range checks {
			i := int(ch.At.UTC().Sub(from) / (24 * time.Hour))
			if i < 0 || i >= days {
				continue
			}
			d := &byDay[i]
			d.Checks++
			total++
			switch ch.Status {
			case Down:
				d.Down++
			case Degraded:
				d.Degraded++
				up++
			default:
				up++
			}
		}
		for i := range byDay {
			d := &byDay[i]
			if d.Checks == 0 {
				continue
			}
			u := float64(d.Checks-d.Down) / float64(d.Checks)
			d.Uptime = &u
			switch {
			case d.Down > 0:
				d.Worst = Down.String()
			case d.Degraded > 0:
				d.Worst = Degraded.String()
			default:
				d.Worst = OK.String()
			}
		}
		if total > 0 {
			u := float64(up) / float64(total)
			v.Uptime = &u
		}
		v.Days = byDay
		out.Components = append(out.Components, v)
	}
	out.Overall = worstNow.String()
	if out.Monitoring.State != "fresh" {
		out.Overall = "unknown"
	}

	incs, err := w.store.Incidents(from)
	if err != nil {
		return nil, err
	}
	for _, in := range incs {
		iv := IncidentView{ID: in.ID, Component: in.Component, Name: names[in.Component], Started: in.Started.Unix(), Worst: in.Worst.String(), Detail: in.Detail}
		if iv.Name == "" {
			iv.Name = in.Component
		}
		if in.Resolved != nil {
			t := in.Resolved.Unix()
			iv.Resolved = &t
		}
		out.Incidents = append(out.Incidents, iv)
	}
	return out, nil
}
