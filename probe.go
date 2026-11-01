package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Status is ordered by severity, so the worst of several is their maximum.
type Status int

const (
	OK Status = iota
	Degraded
	Down
)

func (s Status) String() string {
	switch s {
	case OK:
		return "operational"
	case Degraded:
		return "degraded"
	default:
		return "down"
	}
}

// Result is one observation of one component.
type Result struct {
	Status  Status
	Latency time.Duration
	Detail  string
}

// Component is a thing a visitor would name when saying "it's broken": the
// explorer, the RPC, the NoM node. Not a container, and not a port.
type Component struct {
	ID          string
	Name        string
	Group       string
	Description string
	probe       func(ctx context.Context) (detail string, err error)

	// For the two that report a chain height: the last height seen and when it
	// last moved. A node that answers but has stopped advancing is the failure
	// a plain liveness check misses, and it is the one this chain actually
	// has (a pillar that drops off gossip keeps answering RPC).
	mu       sync.Mutex
	height   int64
	movedAt  time.Time
	stallFor time.Duration
	errs     int64 // the relayer's publishErrors at the previous check; -1 before the first
}

type Prober struct {
	origin string // the gateway, reached inside the compose network
	host   string // the Host the gateway is asked for
	edge   string // the public URL, through Cloudflare; empty disables the edge check
	// The two services with no public path. They are checked by name on the
	// compose network, because a stranger cannot reach them. Empty disables
	// either.
	relayer string
	bridge  string
	// The Telegram bots' pages (rain, bullet) are checked only when named:
	// a bot needs a token to run, so a stack without one, such as
	// zvm-devnet, does not serve them, and listing them anyway made two
	// rows that failed every round.
	bots   map[string]bool
	slow   time.Duration
	client *http.Client
}

func NewProber(origin, host, edge, relayer, bridge string, slow time.Duration) *Prober {
	return &Prober{
		origin:  strings.TrimRight(origin, "/"),
		host:    host,
		edge:    strings.TrimRight(edge, "/"),
		relayer: strings.TrimRight(relayer, "/"),
		bridge:  strings.TrimRight(bridge, "/"),
		slow:    slow,
		// No redirects: every path checked here answers directly, and a
		// redirect to somewhere else is itself the fault worth seeing.
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

// WithBots names the bots whose pages are checked, from a comma-separated
// list; unknown names are ignored.
func (p *Prober) WithBots(list string) *Prober {
	p.bots = map[string]bool{}
	for _, b := range strings.Split(list, ",") {
		if b = strings.ToLower(strings.TrimSpace(b)); b != "" {
			p.bots[b] = true
		}
	}
	return p
}

// Components is the public surface of the gateway, one entry per thing
// the gateway routes. Every probe but the edge goes to the gateway rather than
// through Cloudflare, so a tunnel outage is one red row — the edge — instead
// of all of them, and each other row says whether that service works.
func (p *Prober) Components() []*Component {
	cs := []*Component{
		{ID: "explorer", Name: "Explorer", Group: "Apps", Description: "/explorer — blocks, transactions, momentums", probe: p.app("/explorer/")},
		{ID: "glyphswap", Name: "Glyphswap", Group: "Apps", Description: "/glyphswap — the Uniswap V2 interface", probe: p.app("/glyphswap/")},
		{ID: "karum", Name: "Karum", Group: "Apps", Description: "/karum — the NFT market", probe: p.app("/karum/")},
		{ID: "mozaic", Name: "Mozaic", Group: "Apps", Description: "/mozaic — the shared million-pixel canvas", probe: p.app("/mozaic/")},
		{ID: "rain", Name: "Rain", Group: "Apps", Description: "/rain — linking and approvals for the Telegram rain bot", probe: p.page("/rain/", `id="rain"`)},
		{ID: "bullet", Name: "Bullet", Group: "Apps", Description: "/bullet — Bullet Game, Russian Roulette in Telegram", probe: p.page("/bullet/", `id="bullet"`)},
		{ID: "zchan", Name: "zchan", Group: "Apps", Description: "/zchan — the anonymous image board", probe: p.page("/zchan/", `id="form-slot"`)},
		{ID: "spark", Name: "Spark", Group: "Apps", Description: "/spark — the token launchpad", probe: p.app("/spark/")},
		{ID: "zns", Name: "ZNS", Group: "Apps", Description: "/zns — .znn names, registration and resolution", probe: p.app("/zns/")},
		{ID: "zvm-rpc", Name: "ZVM JSON-RPC", Group: "ZVM", Description: "/zvm/rpc — what an EVM wallet talks to", stallFor: 3 * time.Minute},
		{ID: "zvm-api", Name: "ZVM API", Group: "ZVM", Description: "/zvm/api — the explorer and market API", probe: p.zvmAPI},
		{ID: "faucet", Name: "Faucet", Group: "ZVM", Description: "/zvm/api/faucet — devnet funds on both layers", probe: p.faucet},
		{ID: "nom-http", Name: "NoM JSON-RPC", Group: "Network of Momentum", Description: "/zenon over HTTP — chain 69's znnd", stallFor: 3 * time.Minute},
		{ID: "nom-ws", Name: "NoM WebSocket", Group: "Network of Momentum", Description: "wss /zenon — what Syrius and the SDKs use", probe: p.nomWS},
	}
	kept := cs[:0]
	for _, c := range cs {
		if (c.ID == "rain" || c.ID == "bullet") && !p.bots[c.ID] {
			continue
		}
		kept = append(kept, c)
	}
	cs = kept
	// These two watch their own head, so they are handed their component;
	// found by id, since a new app row shifts every position after it.
	for _, c := range cs {
		switch c.ID {
		case "zvm-rpc":
			c.probe = p.zvmRPC(c)
		case "nom-http":
			c.probe = p.nomHTTP(c)
		}
	}
	// The write path and the peg. Neither is public, but both are what a user
	// is waiting on when a transaction or a withdrawal does not arrive, and
	// the RPC above answers perfectly while either is stuck.
	if p.relayer != "" {
		c := &Component{ID: "relayer", Name: "Relayer", Group: "Rollup services", Description: "batches ZVM transactions onto NoM — where eth_sendRawTransaction ends up", stallFor: 3 * time.Minute, errs: -1}
		c.probe = p.relayerCheck(c)
		cs = append(cs, c)
	}
	if p.bridge != "" {
		cs = append(cs, &Component{ID: "bridge", Name: "Bridge", Group: "Rollup services", Description: "the ZNN/QSR peg — deposits in, withdrawals paid by the FROST committee", probe: p.bridgeCheck})
	}
	if p.edge != "" {
		cs = append(cs, &Component{ID: "edge", Name: "Cloudflare edge", Group: "Edge", Description: "the public URL through the CDN, from outside", probe: p.edgeCheck})
	}
	return cs
}

// Check runs one probe with a deadline and grades it.
func (p *Prober) Check(ctx context.Context, c *Component) Result {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	start := time.Now()
	detail, err := c.probe(ctx)
	r := Result{Latency: time.Since(start), Detail: detail}
	var soft degradedError
	switch {
	case errors.As(err, &soft):
		r.Status, r.Detail = Degraded, soft.msg
	case err != nil:
		r.Status, r.Detail = Down, err.Error()
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		r.Status, r.Detail = Down, "timed out"
	case strings.HasPrefix(detail, stalled):
		r.Status = Degraded
	case r.Latency > p.slow:
		r.Status, r.Detail = Degraded, fmt.Sprintf("slow: %s", r.Latency.Round(time.Millisecond))
	}
	return r
}

const stalled = "stalled"

// degradedError is a probe saying "it answered, and what it said is bad": the
// service is there, so this is not an outage, but someone waiting on it would
// notice.
type degradedError struct{ msg string }

func (e degradedError) Error() string { return e.msg }

func degraded(format string, a ...any) error { return degradedError{fmt.Sprintf(format, a...)} }

// track records a height and reports it as stalled when it has not moved for
// longer than the component allows.
func (c *Component) track(h int64, unit string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if h != c.height || c.movedAt.IsZero() {
		c.height, c.movedAt = h, now
	}
	if idle := now.Sub(c.movedAt); idle > c.stallFor {
		return fmt.Sprintf("%s at %s %d for %s", stalled, unit, h, idle.Round(time.Second))
	}
	return fmt.Sprintf("%s %d", unit, h)
}

// --- probes ---------------------------------------------------------------

func (p *Prober) get(ctx context.Context, path string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.origin+path, nil)
	if err != nil {
		return nil, nil, err
	}
	return p.do(req)
}

func (p *Prober) do(req *http.Request) (*http.Response, []byte, error) {
	if p.host != "" && strings.HasPrefix(req.URL.String(), p.origin) {
		req.Host = p.host
	}
	res, err := p.client.Do(req)
	if err != nil {
		return nil, nil, simplify(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return res, nil, simplify(err)
	}
	if res.StatusCode != http.StatusOK {
		return res, body, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return res, body, nil
}

// A static bundle is up when its shell is served and the shell names a script.
// A missing build answers 404 from the gateway; a half-copied one serves an
// index.html without its entry chunk, which is why the chunk is fetched too.
func (p *Prober) app(path string) func(context.Context) (string, error) {
	return p.page(path, `id="app"`)
}

// page checks a static page the way app checks a Vue app: the HTML carries
// marker, which only that page has, and its entry script is served. Rain's
// page is plain HTML with no #app, so it is recognised by its rain canvas;
// Bullet's by its main element; zchan's by its form slot. zchan's build names its script relative to the
// page ("./assets/…"), so the src is resolved against the page's path.
func (p *Prober) page(path, marker string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		_, body, err := p.get(ctx, path)
		if err != nil {
			return "", err
		}
		html := string(body)
		i := strings.Index(html, `type="module"`)
		if i < 0 || !strings.Contains(html, marker) {
			return "", errors.New("served a page that is not the app")
		}
		j := strings.Index(html[i:], `src="`)
		if j < 0 {
			return "", errors.New("the app's page names no script")
		}
		src := html[i+j+5:]
		src = src[:strings.IndexByte(src, '"')]
		if ref, err := url.Parse(src); err == nil {
			src = (&url.URL{Path: path}).ResolveReference(ref).String()
		}
		if _, _, err := p.get(ctx, src); err != nil {
			return "", fmt.Errorf("entry script %s: %w", src, err)
		}
		return "serving", nil
	}
}

type rpcReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (p *Prober) rpc(ctx context.Context, path, method string, out any) error {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":[]}`, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.origin+path, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	_, raw, err := p.do(req)
	if err != nil {
		return err
	}
	return decodeRPC(raw, out)
}

func decodeRPC(raw []byte, out any) error {
	var r rpcReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return errors.New("answered, but not with JSON-RPC")
	}
	if r.Error != nil {
		return fmt.Errorf("RPC error %d: %s", r.Error.Code, r.Error.Message)
	}
	return json.Unmarshal(r.Result, out)
}

func (p *Prober) zvmRPC(c *Component) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var hex string
		if err := p.rpc(ctx, "/zvm/rpc", "eth_blockNumber", &hex); err != nil {
			return "", err
		}
		h, err := strconv.ParseInt(strings.TrimPrefix(hex, "0x"), 16, 64)
		if err != nil {
			return "", fmt.Errorf("eth_blockNumber returned %q", hex)
		}
		return c.track(h, "block"), nil
	}
}

func (p *Prober) zvmAPI(ctx context.Context) (string, error) {
	_, body, err := p.get(ctx, "/zvm/api/info")
	if err != nil {
		return "", err
	}
	var info struct {
		L2 struct {
			ChainID json.Number `json:"chainId"`
		} `json:"l2"`
	}
	if json.Unmarshal(body, &info) != nil || info.L2.ChainID == "" {
		return "", errors.New("/api/info answered without a chain id")
	}
	return "chain " + info.L2.ChainID.String(), nil
}

func (p *Prober) faucet(ctx context.Context) (string, error) {
	_, body, err := p.get(ctx, "/zvm/api/faucet")
	if err != nil {
		return "", err
	}
	var st struct {
		Address string `json:"address"`
		Served  int64  `json:"served"`
	}
	if json.Unmarshal(body, &st) != nil || st.Address == "" {
		return "", errors.New("answered without a faucet address")
	}
	return fmt.Sprintf("%d served", st.Served), nil
}

type momentum struct {
	Height int64 `json:"height"`
}

func (p *Prober) nomHTTP(c *Component) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var m momentum
		if err := p.rpc(ctx, "/zenon", "ledger.getFrontierMomentum", &m); err != nil {
			return "", err
		}
		return c.track(m.Height, "momentum"), nil
	}
}

func (p *Prober) nomWS(ctx context.Context) (string, error) {
	u, err := url.Parse(p.origin + "/zenon")
	if err != nil {
		return "", err
	}
	u.Scheme = map[string]string{"https": "wss", "http": "ws"}[u.Scheme]
	hdr := http.Header{}
	if p.host != "" {
		hdr.Set("Host", p.host)
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), hdr)
	if err != nil {
		return "", simplify(err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetReadDeadline(dl)
		conn.SetWriteDeadline(dl)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":1,"method":"ledger.getFrontierMomentum","params":[]}`)); err != nil {
		return "", simplify(err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return "", simplify(err)
	}
	var m momentum
	if err := decodeRPC(raw, &m); err != nil {
		return "", err
	}
	return fmt.Sprintf("momentum %d", m.Height), nil
}

func (p *Prober) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	_, body, err := p.do(req)
	if err != nil {
		return err
	}
	if json.Unmarshal(body, out) != nil {
		return errors.New("answered, but not with its status JSON")
	}
	return nil
}

// A relayer that answers is not a relayer that works: the failure this stack
// has actually had is a backlog that stops draining (zvm-fleet's livelock)
// while /status keeps answering 200. So it is graded on movement — something
// queued and nothing posted for three minutes — and on publish errors that
// appeared since the previous check.
func (p *Prober) relayerCheck(c *Component) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var st struct {
			Queued        int64 `json:"queued"`
			Posted        int64 `json:"posted"`
			PublishErrors int64 `json:"publishErrors"`
		}
		if err := p.getJSON(ctx, p.relayer+"/status", &st); err != nil {
			return "", err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		now := time.Now()
		// An empty queue is idle, not stuck, so it resets the clock as well.
		if st.Posted != c.height || st.Queued == 0 || c.movedAt.IsZero() {
			c.height, c.movedAt = st.Posted, now
		}
		var newErrs int64
		if c.errs >= 0 && st.PublishErrors > c.errs {
			newErrs = st.PublishErrors - c.errs
		}
		c.errs = st.PublishErrors
		if idle := now.Sub(c.movedAt); idle > c.stallFor {
			return "", degraded("%d queued, nothing posted for %s", st.Queued, idle.Round(time.Second))
		}
		if newErrs > 0 {
			return "", degraded("%d publish errors since the last check", newErrs)
		}
		return fmt.Sprintf("%d posted · %d queued", st.Posted, st.Queued), nil
	}
}

// Bridge health is a complete five-step report. The aggregate flag, success
// time and every named step must agree; a fresh response alone is insufficient.
var bridgeCriticalSteps = [...]string{
	"receive_deposits", "scan_withdrawals", "pay_withdrawals", "confirm_payouts", "fund_plasma",
}

type bridgeHealthStep struct {
	State      string `json:"state"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
	Error      string `json:"error"`
}

type bridgeHealthReport struct {
	Healthy            bool                        `json:"healthy"`
	LastOkAt           string                      `json:"lastOkAt"`
	LastSweepAt        string                      `json:"lastSweepAt"`
	Sweeping           *bool                       `json:"sweepInProgress"`
	LastError          string                      `json:"lastError"`
	UnreceivedDeposits int                         `json:"unreceivedDeposits"`
	Payouts            map[string]int              `json:"payouts"`
	Steps              map[string]bridgeHealthStep `json:"steps"`
}

func (p *Prober) bridgeCheck(ctx context.Context) (string, error) {
	var st bridgeHealthReport
	if err := p.getJSON(ctx, p.bridge+"/status", &st); err != nil {
		return "", err
	}
	now := time.Now()
	var lastOK time.Time
	if st.LastOkAt != "" {
		var err error
		lastOK, err = time.Parse(time.RFC3339Nano, st.LastOkAt)
		if err != nil {
			return "", errors.New("invalid last successful sweep time")
		}
		since := now.Sub(lastOK)
		if since < -time.Minute {
			return "", errors.New("last successful sweep time is in the future")
		}
		if since > 10*time.Minute {
			msg := fmt.Sprintf("no successful sweep for %s", since.Round(time.Minute))
			if st.LastError != "" {
				msg += ": " + st.LastError
			}
			return "", errors.New(msg)
		}
	}
	if st.Sweeping != nil && *st.Sweeping {
		return "", degraded("bridge sweep is in progress")
	}
	if !st.Healthy {
		return "", degraded("last sweep failed: %s", st.LastError)
	}
	if st.Sweeping == nil {
		return "", degraded("complete sweep activity report unavailable")
	}
	if lastOK.IsZero() {
		return "", errors.New("no successful sweep time")
	}
	for _, name := range bridgeCriticalSteps {
		step := st.Steps[name]
		if step.State == "failed" || step.State == "canceled" {
			return "", degraded("step %s %s: %s", name, step.State, step.Error)
		}
	}
	if len(st.Steps) != len(bridgeCriticalSteps) {
		return "", degraded("complete critical step report unavailable")
	}
	sweep, err := time.Parse(time.RFC3339Nano, st.LastSweepAt)
	if err != nil || !sweep.Truncate(time.Second).Equal(lastOK.Truncate(time.Second)) {
		return "", degraded("last success and completed sweep times disagree")
	}
	if sweep.After(now.Add(time.Minute)) {
		return "", errors.New("last sweep time is in the future")
	}
	var priorFinish time.Time
	for _, name := range bridgeCriticalSteps {
		step, exists := st.Steps[name]
		if !exists || step.State != "succeeded" || step.Error != "" {
			return "", degraded("critical step %s is not a complete success", name)
		}
		start, startErr := time.Parse(time.RFC3339Nano, step.StartedAt)
		finish, finishErr := time.Parse(time.RFC3339Nano, step.FinishedAt)
		if startErr != nil || finishErr != nil || finish.Before(start) || finish.After(sweep) || (!priorFinish.IsZero() && start.Before(priorFinish)) {
			return "", degraded("critical step %s has inconsistent timestamps", name)
		}
		priorFinish = finish
	}
	return fmt.Sprintf("%d paid · %d deposits waiting", st.Payouts["paid"], st.UnreceivedDeposits), nil
}

func (p *Prober) edgeCheck(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.edge+"/zvm/healthz", nil)
	if err != nil {
		return "", err
	}
	res, body, err := p.do(req)
	if err != nil {
		// Cloudflare answers 502/530 itself when the tunnel has no connector.
		if res != nil && res.StatusCode >= 500 {
			return "", fmt.Errorf("%w from Cloudflare — is cloudflared connected?", err)
		}
		return "", err
	}
	if !bytes.Contains(body, []byte(`"ok":true`)) {
		return "", errors.New("healthz answered, but not ok")
	}
	return "reachable", nil
}

// simplify turns Go's nested network errors into the part a person reads.
func simplify(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	s := err.Error()
	switch {
	// Windows says "connectex: No connection could be made because the target
	// machine actively refused it", which is the same fact in 70 more
	// characters, and an alert email is read on a phone.
	case strings.Contains(s, "connection refused"), strings.Contains(s, "actively refused"):
		return errors.New("connection refused")
	case strings.Contains(s, "no such host"):
		return errors.New("DNS: no such host")
	case strings.Contains(s, "deadline exceeded"), strings.Contains(s, "Client.Timeout"):
		return errors.New("timed out")
	}
	return err
}
