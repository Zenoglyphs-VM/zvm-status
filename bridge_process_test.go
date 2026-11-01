//go:build bridgeprocess && linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// This lane starts the shipped Bridge and Status commands. Only their NoM/EVM
// read dependencies are fixtures. No signer, wallet prompt or public endpoint
// is configured, and every unexpected RPC (including a write) fails the test.
type processOutput struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	overflow bool
}

func (o *processOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.buf.Len()+len(p) > 64<<10 {
		o.overflow = true
		return 0, errors.New("fixture process output exceeded its 64 KiB cap")
	}
	return o.buf.Write(p)
}

func (o *processOutput) text(t *testing.T) string {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.overflow || strings.Contains(o.buf.String(), "DATA RACE") {
		t.Fatal("fixture output overflow or child race report")
	}
	return o.buf.String()
}

type ownedProcess struct {
	cmd    *exec.Cmd
	output *processOutput
	done   chan error
	joined bool
}

func processBinary(t *testing.T, key string) string {
	t.Helper()
	path := os.Getenv(key)
	info, err := os.Stat(path)
	if !filepath.IsAbs(path) || err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		t.Fatalf("%s must name an explicitly built executable", key)
	}
	return path
}

func startOwnedProcess(t *testing.T, ctx context.Context, binary string, args ...string) *ownedProcess {
	t.Helper()
	output := new(processOutput)
	cmd := exec.CommandContext(ctx, binary, args...)
	// Do not inherit SMTP, proxy, endpoint, signer or personal configuration.
	cmd.Env = []string{"TZ=UTC", "GOMAXPROCS=2", "GORACE=atexit_sleep_ms=0"}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal("owned fixture command could not start")
	}
	if cmd.Process.Pid == os.Getpid() {
		t.Fatal("fixture command did not start a separate process")
	}
	p := &ownedProcess{cmd: cmd, output: output, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if !p.joined {
			_ = cmd.Process.Kill()
			select {
			case <-p.done:
				p.joined = true
			case <-time.After(3 * time.Second):
				t.Error("owned fixture command did not join after forced cleanup")
			}
		}
		p.output.text(t)
	})
	return p
}

func (p *ownedProcess) wait(t *testing.T, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-p.done:
		p.joined = true
		p.output.text(t)
		return err
	case <-time.After(timeout):
		t.Fatal("owned fixture command exceeded its completion deadline")
		return nil
	}
}

type bridgeRPCFixture struct {
	mu         sync.Mutex
	mode       string
	release    chan struct{}
	unexpected []string
}

func (f *bridgeRPCFixture) modeTo(mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.release != nil {
		close(f.release)
		f.release = nil
	}
	f.mode = mode
	if mode == "blocked" {
		f.release = make(chan struct{})
	}
}

func (f *bridgeRPCFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request) != nil {
		http.Error(w, "invalid fixture request", http.StatusBadRequest)
		return
	}
	response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
	var result any
	switch request.Method {
	case "eth_blockNumber":
		result = "0x1"
	case "eth_getBlockByNumber":
		f.mu.Lock()
		mode, release := f.mode, f.release
		f.mu.Unlock()
		if mode == "blocked" {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if mode == "failed" {
			response["error"] = map[string]any{"code": -32000, "message": "controlled finalized-header refusal"}
		} else {
			zero := "0x" + strings.Repeat("0", 64)
			result = map[string]any{
				"parentHash": zero, "sha3Uncles": zero, "miner": "0x" + strings.Repeat("0", 40),
				"stateRoot": zero, "transactionsRoot": zero, "receiptsRoot": zero,
				"logsBloom": "0x" + strings.Repeat("0", 512), "difficulty": "0x0", "number": "0x1",
				"gasLimit": "0x1c9c380", "gasUsed": "0x0", "timestamp": "0x1", "extraData": "0x",
				"mixHash": zero, "nonce": "0x0000000000000000", "baseFeePerGas": "0x0"}
		}
	case "eth_getLogs":
		result = []any{}
	case "eth_call":
		result = "0x" + strings.Repeat("0", 64)
	case "eth_getBalance":
		result = "0x0"
	case "ledger.getAccountInfoByAddress":
		result = map[string]any{"accountHeight": 0, "balanceInfoMap": map[string]any{}}
	case "ledger.getUnreceivedBlocksByAddress":
		result = map[string]any{"count": 0, "list": []any{}, "more": false}
	case "ledger.getFrontierMomentum":
		result = map[string]any{"height": 1}
	default:
		f.mu.Lock()
		f.unexpected = append(f.unexpected, request.Method)
		f.mu.Unlock()
		response["error"] = map[string]any{"code": -32601, "message": "RPC is outside this read-only fixture"}
	}
	if response["error"] == nil {
		response["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func TestBridgeStatusProcesses(t *testing.T) {
	bridgeBinary := processBinary(t, "B02_PROCESS_BRIDGE")
	statusBinary := processBinary(t, "B02_PROCESS_STATUS")
	config := os.Getenv("B02_PROCESS_ROLLUP")
	if !filepath.IsAbs(config) {
		t.Fatal("the pinned public devnet rollup configuration is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := &bridgeRPCFixture{mode: "ok"}
	rpc := httptest.NewServer(fixture)
	defer rpc.Close()
	defer fixture.modeTo("ok")
	// The production CLI owns its listener; release the selected loopback port
	// immediately before starting it. A bind failure fails this attempt.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	producer := startOwnedProcess(t, ctx, bridgeBinary, "bridge", "-config", config,
		"-l1", rpc.URL, "-eth", rpc.URL, "-db", filepath.Join(t.TempDir(), "bridge.db"),
		"-listen", address, "-poll", "10s", "-fund-plasma=false")
	origin := "http://" + address
	client := &http.Client{Timeout: time.Second}
	type state struct {
		Healthy   bool   `json:"healthy"`
		Sweeping  bool   `json:"sweepInProgress"`
		LastOK    string `json:"lastOkAt"`
		LastSweep string `json:"lastSweepAt"`
		Steps     map[string]struct {
			State string `json:"state"`
		} `json:"steps"`
	}
	await := func(t *testing.T, predicate func(state) bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case <-producer.done:
				producer.joined = true
				t.Fatal("Bridge command exited before the required state")
			default:
			}
			response, err := client.Get(origin + "/status")
			if err == nil {
				var s state
				err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&s)
				response.Body.Close()
				finished, finishErr := time.Parse(time.RFC3339Nano, s.LastSweep)
				// A completed sample is accepted only inside the first second
				// of the ten-second idle window. The observer has five seconds,
				// so an expected success cannot overlap a later scheduled sweep.
				freshWindow := s.Sweeping || (finishErr == nil && time.Since(finished) >= 0 && time.Since(finished) < time.Second)
				if err == nil && response.StatusCode == http.StatusOK && len(s.Steps) == 5 && freshWindow && predicate(s) {
					return
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal("fixture lifetime expired")
			case <-time.After(20 * time.Millisecond):
			}
		}
		t.Fatal("the production Bridge status did not establish the required state")
	}
	observe := func(t *testing.T, want, detail string) {
		t.Helper()
		childCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		consumer := startOwnedProcess(t, childCtx, statusBinary, "-origin", rpc.URL,
			"-bridge", origin, "-slow", "5s", "-once")
		if consumer.cmd.Process.Pid == producer.cmd.Process.Pid {
			t.Fatal("producer and consumer share a process")
		}
		if err := consumer.wait(t, 6*time.Second); err != nil {
			t.Fatal("Status command failed")
		}
		found := 0
		for _, line := range strings.Split(consumer.output.text(t), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "bridge" {
				found++
				if len(fields) < 4 || fields[1] != want || !strings.Contains(strings.Join(fields[3:], " "), detail) {
					t.Fatalf("production Status command did not report %s with the expected cause", want)
				}
			}
		}
		if found != 1 {
			t.Fatal("Status command did not produce exactly one bridge observation")
		}
		evidence, _ := json.Marshal(map[string]any{"case": t.Name(), "producer_pin": "3734f2e967933952c8f513cfcac355e3e4b5f242",
			"producer_entrypoint": "zvm-bridge bridge", "consumer_entrypoint": "zvm-status -once",
			"distinct_processes": true, "consumer_actual_exit": 0, "bridge_result": want,
			"dependencies": "loopback read-only NoM/EVM fixtures", "other_components_qualified": false})
		t.Log("B02_PROCESS_EVIDENCE " + string(evidence))
	}
	if !t.Run("complete_sweep", func(t *testing.T) {
		await(t, func(s state) bool { return s.Healthy && !s.Sweeping && s.LastOK != "" })
		observe(t, "operational", "paid")
	}) {
		return
	}
	if !t.Run("failed_step_after_later_noop", func(t *testing.T) {
		fixture.modeTo("failed")
		await(t, func(s state) bool {
			return !s.Healthy && !s.Sweeping && s.Steps["scan_withdrawals"].State == "failed" && s.Steps["fund_plasma"].State == "succeeded"
		})
		observe(t, "degraded", "last sweep failed")
	}) {
		return
	}
	if !t.Run("in_progress_after_prior_success", func(t *testing.T) {
		fixture.modeTo("blocked")
		await(t, func(s state) bool {
			return !s.Healthy && s.Sweeping && s.LastOK != "" && s.Steps["scan_withdrawals"].State == "running"
		})
		observe(t, "degraded", "sweep is in progress")
	}) {
		return
	}
	if !t.Run("complete_recovery", func(t *testing.T) {
		fixture.modeTo("ok")
		await(t, func(s state) bool { return s.Healthy && !s.Sweeping })
		observe(t, "operational", "paid")
	}) {
		return
	}
	if !t.Run("stopped_producer", func(t *testing.T) {
		if err := producer.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		err := producer.wait(t, 8*time.Second)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(producer.output.text(t), "context canceled") {
			t.Fatal("Bridge did not terminate through its production cancellation path")
		}
		observe(t, "down", "connection refused")
	}) {
		return
	}
	fixture.mu.Lock()
	unexpected := append([]string(nil), fixture.unexpected...)
	fixture.mu.Unlock()
	if len(unexpected) != 0 {
		t.Fatal("unexpected or write RPC attempted against the fixture")
	}
	fmt.Println("B02_PROCESS_CLOSED producer_joined=true consumer_processes_joined=5 unexpected_RPCs=0 public_RPCs=0")
}
