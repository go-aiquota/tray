// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aiquota/tray/stamp"
)

var t0 = time.Date(2026, 11, 21, 6, 59, 14, 0, time.UTC)

// fixture is one account's two windows: used5/used7 percent, 2 h left in the
// 5 h window (60 % elapsed) and 5 d left in the 7 d window (28.6 % elapsed).
type fixture struct {
	account      string
	used5, used7 float64
	verdict      stamp.Verdict
}

func writeStamp(t *testing.T, path string, at time.Time, fs ...fixture) {
	t.Helper()
	st := stamp.Stamp{Version: stamp.Version, At: at, Verdict: stamp.Available}
	for _, f := range fs {
		v := f.verdict
		if v == "" {
			v = stamp.Available
		}
		st.Windows = append(st.Windows,
			stamp.Window{Account: f.account, Label: "Session (5hr)", Used: f.used5, Limit: 100, UsedPercent: f.used5, ResetsAtUnix: t0.Add(2 * time.Hour).Unix(), Verdict: v},
			stamp.Window{Account: f.account, Label: "Weekly (7 day)", Used: f.used7, Limit: 100, UsedPercent: f.used7, ResetsAtUnix: t0.Add(120 * time.Hour).Unix(), Verdict: v},
			stamp.Window{Account: f.account, Label: "Weekly Opus", Verdict: stamp.NotAvailable})
	}
	if err := stamp.Write(path, st); err != nil {
		t.Fatal(err)
	}
}

type env struct {
	dir string
}

func newEnv(t *testing.T) env {
	t.Helper()
	old := now
	now = func() time.Time { return t0 }
	t.Cleanup(func() { now = old })
	return env{dir: t.TempDir()}
}

func (e env) flags() []string {
	return []string{"--stamp", filepath.Join(e.dir, "stamp.json"), "--gate", filepath.Join(e.dir, "gate.json"), "--waiver-log", filepath.Join(e.dir, "waivers.log")}
}

func (e env) stamp(t *testing.T, fs ...fixture) {
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), t0, fs...)
}

func (e env) run(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errw bytes.Buffer
	code := run(append(args, e.flags()...), strings.NewReader(stdin), &out, &errw)
	return out.String(), errw.String(), code
}

func spawn(subagent, model, prompt string) string {
	in := map[string]any{"tool_name": "Agent", "tool_input": map[string]string{"subagent_type": subagent, "model": model, "prompt": prompt}}
	b, _ := json.Marshal(in)
	return string(b)
}

func (e env) hook(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, errw, code := e.run(t, stdin, args...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errw)
	}
	return out
}

func denied(t *testing.T, out string) string {
	t.Helper()
	var d struct {
		H struct {
			Event, Decision, Reason string
		} `json:"hookSpecificOutput"`
	}
	var raw map[string]map[string]string
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("not a deny JSON: %q: %v", out, err)
	}
	h := raw["hookSpecificOutput"]
	d.H.Event, d.H.Decision, d.H.Reason = h["hookEventName"], h["permissionDecision"], h["permissionDecisionReason"]
	if d.H.Event != "PreToolUse" || d.H.Decision != "deny" || d.H.Reason == "" {
		t.Fatalf("bad deny: %q", out)
	}
	return d.H.Reason
}

var over = fixture{account: "Work", used5: 30, used7: 62}
var fine = fixture{account: "Home", used5: 30, used7: 20}

func TestNoStampAllows(t *testing.T) {
	e := newEnv(t)
	if out := e.hook(t, spawn("implementer", "sonnet", "x")); out != "" {
		t.Fatalf("no stamp must allow, got %q", out)
	}
}

func TestStaleStampAllows(t *testing.T) {
	e := newEnv(t)
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), t0.Add(-time.Hour), over)
	if out := e.hook(t, spawn("implementer", "sonnet", "x")); out != "" {
		t.Fatalf("stale stamp must allow, got %q", out)
	}
	if out := e.hook(t, spawn("implementer", "sonnet", "x"), "--max-age", "2h"); out == "" {
		t.Fatal("the same stamp within --max-age must deny")
	}
}

func TestOverPaceDenies(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over)
	reason := denied(t, e.hook(t, spawn("implementer", "sonnet", "x")))
	for _, want := range []string{"7d: used 62.0%", "28.6% of the window elapsed", "sustainable 0.32%/h", "rate 1.29%/h", "projected exhaustion", "Work", "pace-waiver:", "quotapace pace off"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason lacks %q:\n%s", want, reason)
		}
	}
	if strings.Contains(reason, "5h:") {
		t.Errorf("the healthy 5h window must not be listed:\n%s", reason)
	}
}

func TestUnderPaceAllows(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, fine)
	if out := e.hook(t, spawn("implementer", "sonnet", "x")); out != "" {
		t.Fatalf("under pace must allow, got %q", out)
	}
}

func TestToleranceBoundary(t *testing.T) {
	e := newEnv(t)
	// 7d: 28.57 % elapsed; 31 % used projects to 108.5 %, 32 % to 112 %.
	e.stamp(t, fixture{account: "Work", used5: 10, used7: 31})
	if out := e.hook(t, spawn("implementer", "sonnet", "x")); out != "" {
		t.Fatalf("within tolerance must allow, got %q", out)
	}
	e.stamp(t, fixture{account: "Work", used5: 10, used7: 32})
	denied(t, e.hook(t, spawn("implementer", "sonnet", "x")))
}

func TestFiveHourWindowDenies(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, fixture{account: "Work", used5: 69, used7: 10})
	reason := denied(t, e.hook(t, spawn("implementer", "sonnet", "x")))
	if !strings.Contains(reason, "5h: used 69.0%") || strings.Contains(reason, "7d:") {
		t.Fatalf("reason: %s", reason)
	}
}

func TestExhaustedDenies(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, fixture{account: "Work", used5: 100, used7: 10, verdict: stamp.Exhausted})
	if reason := denied(t, e.hook(t, spawn("implementer", "sonnet", "x"))); !strings.Contains(reason, "exhausted") {
		t.Fatal(reason)
	}
}

func TestCheapSpawnsAllow(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over)
	for _, in := range []string{spawn("checker", "", "x"), spawn("general-purpose", "haiku", "x"), spawn("", "Haiku", "x"),
		`{"tool_name":"Bash","tool_input":{"command":"ls"}}`} {
		if out := e.hook(t, in); out != "" {
			t.Errorf("%s must allow, got %q", in, out)
		}
	}
}

func TestWaiverAllowsAndIsLogged(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over)
	out := e.hook(t, spawn("implementer", "sonnet", "do it\n  pace-waiver: release blocked on this\nmore"))
	if out != "" {
		t.Fatalf("waiver must allow, got %q", out)
	}
	log, err := os.ReadFile(filepath.Join(e.dir, "waivers.log"))
	if err != nil || !strings.Contains(string(log), "release blocked on this") {
		t.Fatalf("waiver not logged: %q %v", log, err)
	}
	denied(t, e.hook(t, spawn("implementer", "sonnet", "pace-waiver:   "))) // an empty reason is no waiver
}

func TestOffOnSwitch(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over)
	if out, _, _ := e.run(t, "", "pace", "off"); !strings.Contains(out, "off until `pace on`") {
		t.Fatalf("status after off: %q", out)
	}
	if out := e.hook(t, spawn("implementer", "sonnet", "x")); out != "" {
		t.Fatalf("off must allow silently, got %q", out)
	}
	e.run(t, "", "pace", "on")
	denied(t, e.hook(t, spawn("implementer", "sonnet", "x")))
}

func TestOffForExpires(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over)
	out, _, _ := e.run(t, "", "pace", "off", "--for", "2h")
	if !strings.Contains(out, "off until") || strings.Contains(out, "pace on") {
		t.Fatalf("status must show the expiry: %q", out)
	}
	if out := e.hook(t, spawn("implementer", "sonnet", "x")); out != "" {
		t.Fatalf("inside the window must allow, got %q", out)
	}
	now = func() time.Time { return t0.Add(2*time.Hour + time.Second) }
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), now(), over)
	denied(t, e.hook(t, spawn("implementer", "sonnet", "x")))
	if out, _, _ := e.run(t, "", "pace", "status"); strings.Contains(out, "off") {
		t.Fatalf("an expired switch must read on: %q", out)
	}
}

func TestOffPerAccount(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over, fixture{account: "Other", used5: 40, used7: 70})
	denied(t, e.hook(t, spawn("implementer", "sonnet", "x"))) // both over pace
	e.run(t, "", "pace", "off", "--account", "Work")
	if out := e.hook(t, spawn("implementer", "sonnet", "x"), "--account", "Work"); out != "" {
		t.Fatalf("Work is off, got %q", out)
	}
	reason := denied(t, e.hook(t, spawn("implementer", "sonnet", "x"), "--account", "Other"))
	if strings.Contains(reason, "Work") {
		t.Fatalf("Other's refusal must not name Work: %s", reason)
	}
	if out, _, _ := e.run(t, "", "pace", "status"); !strings.Contains(out, "account Work: off") || !strings.Contains(out, "gate: on") {
		t.Fatalf("status: %q", out)
	}
	e.run(t, "", "pace", "on", "--account", "Work")
	denied(t, e.hook(t, spawn("implementer", "sonnet", "x"), "--account", "Work"))
}

func TestAccountSelection(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over, fine)
	if out := e.hook(t, spawn("implementer", "sonnet", "x")); out != "" {
		t.Fatalf("one account with headroom must allow, got %q", out)
	}
	denied(t, e.hook(t, spawn("implementer", "sonnet", "x"), "--account", "Work"))
	if out := e.hook(t, spawn("implementer", "sonnet", "x"), "--account", "Nobody"); out != "" {
		t.Fatalf("unknown account must allow, got %q", out)
	}
}

func TestUnreadableInputsAllow(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over)
	if err := os.WriteFile(filepath.Join(e.dir, "gate.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := e.hook(t, spawn("implementer", "sonnet", "x")); out != "" {
		t.Fatalf("a broken switch file must allow, got %q", out)
	}
	if _, _, code := e.run(t, "not json"); code == 0 {
		t.Fatal("bad hook JSON is a (non-blocking) error")
	}
}

func TestReportJSONSchema(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over, fine)
	e.run(t, "", "pace", "off", "--for", "2h", "--account", "Home")
	out := e.hook(t, "", "pace", "--json")
	var r map[string]any
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schema", "at", "stamp_at", "stamp_age_seconds", "stamp_fresh", "gate", "accounts", "aggregate"} {
		if _, ok := r[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}
	accts := r["accounts"].([]any)
	if len(accts) != 2 || r["schema"].(float64) != 1 {
		t.Fatalf("report: %s", out)
	}
	w := accts[0].(map[string]any)["windows"].([]any)[1].(map[string]any)
	for _, k := range []string{"window", "used_percent", "elapsed_percent", "rate_per_hour", "sustainable_per_hour", "projected_at_reset_percent", "projected_exhaustion", "resets_at", "state"} {
		if _, ok := w[k]; !ok {
			t.Errorf("missing window key %q", k)
		}
	}
	if w["window"] != "7d" || w["state"] != "over_pace" || accts[0].(map[string]any)["account"] != "Work" {
		t.Errorf("window: %v", w)
	}
	home := accts[1].(map[string]any)["gate"].(map[string]any)
	if home["state"] != "off" || home["until"] == nil {
		t.Errorf("Home gate: %v", home)
	}
	if agg := r["aggregate"].([]any); len(agg) != 2 {
		t.Errorf("aggregate: %v", agg)
	}
}

func TestReportTextShowsGateOffAndKeepsMeasuring(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over)
	e.run(t, "", "pace", "off", "--for", "2h")
	out := e.hook(t, "", "pace")
	for _, want := range []string{"gate: off until", "Work (gate: on)", "used  62.0%", "[over_pace]", "aggregate"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if _, _, code := newEnv(t).run(t, "", "pace"); code == 0 {
		t.Error("a report with no stamp is an error")
	}
}

func TestComputeTable(t *testing.T) {
	end := t0.Add(120 * time.Hour)
	if p := compute("7d", 168*time.Hour, 5, end, t0, 0.05, 0.1); p.State != stateOK || p.RatePerHour < 0.1 {
		t.Errorf("ok: %+v", p)
	}
	if p := compute("7d", 168*time.Hour, 5, t0.Add(167*time.Hour), t0, 0.05, 0.1); p.State != stateTooEarly || p.ProjectedExhaustion != nil {
		t.Errorf("too early: %+v", p)
	}
	p := compute("7d", 168*time.Hour, 62, end, t0, 0.05, 0.1)
	if p.State != stateOverPace || p.ProjectedExhaustion == nil || !p.ProjectedExhaustion.Before(end) {
		t.Errorf("over: %+v", p)
	}
	if p := compute("5h", 5*time.Hour, 50, t0.Add(-time.Minute), t0, 0.05, 0.1); p.State != stateOK || p.SustainablePerHour != 0 {
		t.Errorf("past reset: %+v", p)
	}
}

func TestVersionFlag(t *testing.T) {
	e := newEnv(t)
	if out := e.hook(t, "", "--version"); !strings.HasPrefix(out, "quotapace ") {
		t.Fatalf("version: %q", out)
	}
}

func TestStampExactlyMaxAgeIsStillRead(t *testing.T) {
	e := newEnv(t)
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), t0.Add(-15*time.Minute), over)
	denied(t, e.hook(t, spawn("implementer", "sonnet", "x"))) // age == --max-age: still fresh
}

func TestSwitchErrors(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over)
	if _, _, code := e.run(t, "", "pace", "off", "--for", "-1h"); code == 0 {
		t.Error("a negative --for must be refused")
	}
	if out, _, _ := e.run(t, "", "pace", "status"); out != "gate: on\n" {
		t.Errorf("a refused off must leave the gate on: %q", out)
	}
	if err := os.WriteFile(filepath.Join(e.dir, "gate.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"pace"}, {"pace", "status"}, {"pace", "off"}, {"pace", "on"}, {"pace", "off", "--account", "A"}} {
		if _, _, code := e.run(t, "", args...); code == 0 {
			t.Errorf("%v on a broken switch file must fail", args)
		}
	}
	// A switch file that cannot be written (its directory is a file).
	blocker := filepath.Join(e.dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"pace", "off", "--gate", filepath.Join(blocker, "g.json")}, strings.NewReader(""), &out, &errw); code == 0 {
		t.Error("an unwritable switch file must fail")
	}
}

func TestIndefiniteOffIsReported(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over)
	e.run(t, "", "pace", "off")
	if out := e.hook(t, "", "pace"); !strings.Contains(out, "gate: off until `pace on`") {
		t.Fatalf("report: %s", out)
	}
}
