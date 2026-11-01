package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func completeBridgeReport(now time.Time) string {
	at := now.Add(-time.Second).UTC().Truncate(time.Millisecond)
	steps := map[string]any{}
	for i, name := range []string{"receive_deposits", "scan_withdrawals", "pay_withdrawals", "confirm_payouts", "fund_plasma"} {
		start := at.Add(-time.Second + time.Duration(i)*100*time.Millisecond)
		steps[name] = map[string]any{"state": "succeeded", "startedAt": start.Format(time.RFC3339Nano), "finishedAt": start.Add(50 * time.Millisecond).Format(time.RFC3339Nano)}
	}
	raw, _ := json.Marshal(map[string]any{"healthy": true, "lastOkAt": at.Format(time.RFC3339), "lastSweepAt": at.Format(time.RFC3339Nano), "sweepInProgress": false, "steps": steps, "payouts": map[string]int{"paid": 6}})
	return string(raw)
}

func TestBridgeCompleteSweepGrading(t *testing.T) {
	type object = map[string]any
	tests := []struct {
		name   string
		mutate func(object)
		want   Status
		detail string
	}{
		{"complete", func(object) {}, OK, "6 paid"},
		{"legacy aggregate alone", func(v object) { delete(v, "steps") }, Degraded, "complete critical step report unavailable"},
		{"missing activity field", func(v object) { delete(v, "sweepInProgress") }, Degraded, "complete sweep activity report unavailable"},
		{"in progress with false aggregate", func(v object) { v["sweepInProgress"], v["healthy"] = true, false }, Degraded, "in progress"},
		{"in progress", func(v object) { v["sweepInProgress"] = true }, Degraded, "in progress"},
		{"missing success time", func(v object) { delete(v, "lastOkAt") }, Down, "no successful sweep time"},
		{"invalid success time", func(v object) { v["lastOkAt"] = "invalid" }, Down, "invalid last successful sweep time"},
		{"future success time", func(v object) { v["lastOkAt"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339) }, Down, "sweep time is in the future"},
		{"stale success time", func(v object) { v["lastOkAt"] = time.Now().Add(-25 * time.Minute).UTC().Format(time.RFC3339) }, Down, "no successful sweep for"},
		{"missing completed sweep", func(v object) { delete(v, "lastSweepAt") }, Degraded, "times disagree"},
		{"different completed sweep", func(v object) { v["lastSweepAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano) }, Degraded, "times disagree"},
		{"extra unknown step", func(v object) { v["steps"].(object)["unknown"] = object{"state": "succeeded"} }, Degraded, "complete critical step report unavailable"},
		{"canceled", func(v object) { v["steps"].(object)["confirm_payouts"].(object)["state"] = "canceled" }, Degraded, "confirm_payouts canceled"},
		{"not run", func(v object) { v["steps"].(object)["pay_withdrawals"].(object)["state"] = "not_run" }, Degraded, "pay_withdrawals is not a complete success"},
		{"unknown state", func(v object) { v["steps"].(object)["pay_withdrawals"].(object)["state"] = "available" }, Degraded, "pay_withdrawals is not a complete success"},
		{"error beside success", func(v object) { v["steps"].(object)["pay_withdrawals"].(object)["error"] = "fixture refused" }, Degraded, "pay_withdrawals is not a complete success"},
		{"missing step start", func(v object) { delete(v["steps"].(object)["pay_withdrawals"].(object), "startedAt") }, Degraded, "pay_withdrawals has inconsistent timestamps"},
		{"reversed step", func(v object) {
			s := v["steps"].(object)["pay_withdrawals"].(object)
			s["startedAt"], s["finishedAt"] = s["finishedAt"], s["startedAt"]
		}, Degraded, "pay_withdrawals has inconsistent timestamps"},
		{"step after sweep", func(v object) {
			v["steps"].(object)["fund_plasma"].(object)["finishedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		}, Degraded, "fund_plasma has inconsistent timestamps"},
		{"overlapping ordered steps", func(v object) {
			v["steps"].(object)["pay_withdrawals"].(object)["startedAt"] = v["steps"].(object)["receive_deposits"].(object)["startedAt"]
		}, Degraded, "pay_withdrawals has inconsistent timestamps"},
	}
	for _, name := range []string{"receive_deposits", "scan_withdrawals", "pay_withdrawals", "confirm_payouts", "fund_plasma"} {
		name := name
		tests = append(tests,
			struct {
				name   string
				mutate func(object)
				want   Status
				detail string
			}{"failed " + name, func(v object) {
				s := v["steps"].(object)[name].(object)
				s["state"], s["error"] = "failed", "fixture refused"
			}, Degraded, name + " failed: fixture refused"},
			struct {
				name   string
				mutate func(object)
				want   Status
				detail string
			}{"missing " + name, func(v object) { delete(v["steps"].(object), name) }, Degraded, "complete critical step report unavailable"})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var value object
			if err := json.Unmarshal([]byte(completeBridgeReport(time.Now().UTC())), &value); err != nil {
				t.Fatal(err)
			}
			tc.mutate(value)
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			srv := serve(t, 200, func() string { return string(raw) })
			p := NewProber("http://unused", "", "", "", srv.URL, time.Second)
			result := p.Check(context.Background(), component(p, "bridge"))
			if result.Status != tc.want || !strings.Contains(result.Detail, tc.detail) {
				t.Fatalf("got %s (%s), want %s containing %q", result.Status, result.Detail, tc.want, tc.detail)
			}
		})
	}
}
