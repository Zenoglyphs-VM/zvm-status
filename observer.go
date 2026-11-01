package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type monitoringReply struct {
	Schema     int        `json:"schema"`
	OK         bool       `json:"ok"`
	Monitoring Monitoring `json:"monitoring"`
}

// observeStatus is a one-shot check for a separately operated observer. The
// caller's clock and age budget constrain replayed replies independently of
// an HTTP 200 or repeated successful requests. It sends no notifications.
func observeStatus(ctx context.Context, endpoint string, maxAge time.Duration, requireRelay bool, clock func() time.Time) error {
	if maxAge < time.Second {
		return errors.New("observer age budget must be at least one second")
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("observer requires an explicit HTTP(S) monitoring endpoint without user information")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errors.New("invalid monitoring request")
	}
	req.Header.Set("Cache-Control", "no-cache")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("monitoring endpoint is unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New("monitoring endpoint is not healthy")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (32<<10)+1))
	if err != nil || len(data) > 32<<10 {
		return errors.New("monitoring response is incomplete or oversized")
	}
	var reply monitoringReply
	d := json.NewDecoder(bytes.NewReader(data))
	if d.Decode(&reply) != nil || d.Decode(new(any)) != io.EOF || reply.Schema != 1 {
		return errors.New("unsupported monitoring response")
	}
	now := clock()
	m := reply.Monitoring
	if !reply.OK || m.State != "fresh" || m.Failure != "" {
		return errors.New("monitoring has not established a fresh successful round")
	}
	seconds := int64(maxAge / time.Second)
	if m.CheckedAt <= 0 || m.CheckedAt > now.Unix() || m.FreshUntil <= m.CheckedAt || m.FreshUntil <= now.Unix() || now.Unix()-m.CheckedAt > seconds || m.FreshUntil-m.CheckedAt > seconds {
		return errors.New("monitoring evidence is expired or outside the independent age budget")
	}
	n := m.Notifications
	if n.State == "failed" {
		return errors.New("notification path reports a failed relay attempt")
	}
	if requireRelay && (n.State != "accepted" || n.AcceptedAt <= 0 || n.AcceptedAt > now.Unix() || n.AcceptedAt < n.AttemptedAt || now.Unix()-n.AcceptedAt > seconds) {
		return errors.New("recent SMTP relay acceptance has not been established")
	}
	return nil
}

func observerCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	endpoint := fs.String("url", "", "explicit monitoring /healthz endpoint")
	maxAge := fs.Duration("max-age", 11*time.Minute, "independent maximum age and declared freshness window")
	relay := fs.Bool("require-relay-acceptance", false, "also require a recent observed SMTP relay acceptance; does not prove recipient delivery")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return errors.New("invalid observer arguments")
	}
	if err := observeStatus(context.Background(), *endpoint, *maxAge, *relay, time.Now); err != nil {
		return err
	}
	_, err := fmt.Fprintln(out, "Probe freshness verified. Recipient delivery and independent observer placement remain operator acceptance requirements.")
	return err
}
