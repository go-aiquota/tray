// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	plan         string  // "" is team; noPlan is an unknown plan
	opus7        float64 // > 0: the plan has a Weekly Opus window at this percent (else it is listed n/a)
}

const noPlan = "-"

func (f fixture) planName() string {
	switch f.plan {
	case "":
		return "team"
	case noPlan:
		return ""
	}
	return f.plan
}

func (f fixture) key() string { return accountKey(f.account, f.planName()) }

func writeStamp(t *testing.T, path string, at time.Time, fs ...fixture) {
	t.Helper()
	st := stamp.Stamp{Version: stamp.Version, At: at, Verdict: stamp.Available}
	for _, f := range fs {
		v := f.verdict
		if v == "" {
			v = stamp.Available
		}
		p := f.planName()
		opus := stamp.Window{Account: f.account, Plan: p, Label: "Weekly Opus", Verdict: stamp.NotAvailable}
		if f.opus7 > 0 {
			opus = stamp.Window{Account: f.account, Plan: p, Label: "Weekly Opus", Used: f.opus7, Limit: 100, UsedPercent: f.opus7, ResetsAtUnix: t0.Add(120 * time.Hour).Unix(), Verdict: stamp.Available}
		}
		st.Windows = append(st.Windows,
			stamp.Window{Account: f.account, Plan: p, Label: "Session (5hr)", Used: f.used5, Limit: 100, UsedPercent: f.used5, ResetsAtUnix: t0.Add(2 * time.Hour).Unix(), Verdict: v},
			stamp.Window{Account: f.account, Plan: p, Label: "Weekly (7 day)", Used: f.used7, Limit: 100, UsedPercent: f.used7, ResetsAtUnix: t0.Add(120 * time.Hour).Unix(), Verdict: v},
			opus)
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
	dir := t.TempDir()
	// Whatever a test (or a mutant of the code under test) resolves by default
	// lands in the temp dir, never in the owner's real config directory.
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
	// No test starts the real refresher: the default one counts and fails.
	oldStart := startRefresh
	refreshCalls = 0
	startRefresh = func(string, []string, time.Duration) error {
		refreshCalls++
		return errors.New("no refresher in tests")
	}
	t.Cleanup(func() { startRefresh = oldStart })
	return env{dir: dir}
}

// refreshCalls counts the refreshes the code under test asked for.
var refreshCalls int

func (e env) flags() []string {
	return []string{"--stamp", filepath.Join(e.dir, "stamp.json"), "--gate", filepath.Join(e.dir, "gate.json"), "--waiver-log", filepath.Join(e.dir, "waivers.log"),
		"--history", filepath.Join(e.dir, "history.json"), "--refresh-state", filepath.Join(e.dir, "refresh.json")}
}

// burn records that f's account was burning r5 and r7 percent per hour until
// the stamp at t0: one earlier reading 30 minutes before it.
func (e env) burn(t *testing.T, r5, r7 float64, fs ...fixture) {
	t.Helper()
	e.burnAt(t, t0, r5, r7, fs...)
}

// burnAt is burn for a stamp taken at end.
func (e env) burnAt(t *testing.T, end time.Time, r5, r7 float64, fs ...fixture) {
	t.Helper()
	h := readHistory(filepath.Join(e.dir, "history.json"))
	for _, f := range fs {
		h.Samples = append(h.Samples,
			Sample{At: end.Add(-30 * time.Minute), Account: f.key(), Window: "5h", Used: f.used5 - r5/2, ResetsAt: t0.Add(2 * time.Hour)},
			Sample{At: end.Add(-30 * time.Minute), Account: f.key(), Window: "7d", Used: f.used7 - r7/2, ResetsAt: t0.Add(120 * time.Hour)})
		if f.opus7 > 0 {
			h.Samples = append(h.Samples, Sample{At: end.Add(-30 * time.Minute), Account: f.key(), Window: "7d opus", Used: f.opus7 - 10, ResetsAt: t0.Add(120 * time.Hour)})
		}
	}
	if err := writeHistory(filepath.Join(e.dir, "history.json"), h); err != nil {
		t.Fatal(err)
	}
}

// hot is the stamp and a burst on every window that makes 7d over pace.
func (e env) hot(t *testing.T, fs ...fixture) {
	t.Helper()
	e.stamp(t, fs...)
	e.burn(t, 20, 20, fs...)
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

// opusSpawn is the delegation the gate refuses over pace: an opus reviewer.
func opusSpawn(prompt string) string { return spawn("reviewer", "opus", prompt) }

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
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("no stamp must allow, got %q", out)
	}
}

func TestStaleStampAllows(t *testing.T) {
	e := newEnv(t)
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), t0.Add(-time.Hour), over)
	e.burnAt(t, t0.Add(-time.Hour), 20, 20, over)
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("stale stamp must allow, got %q", out)
	}
	if out := e.hook(t, opusSpawn("x"), "--max-age", "2h"); out == "" {
		t.Fatal("the same stamp within --max-age must deny")
	}
}

func TestOverPaceDenies(t *testing.T) {
	e := newEnv(t)
	e.hot(t, over)
	reason := denied(t, e.hook(t, opusSpawn("x")))
	for _, want := range []string{"7d: used 62.0%", "28.6% of the window elapsed", "sustainable 0.32%/h", "cumulative 1.29%/h", "recent 20.00%/h", "gone by", "Work", "pace-waiver:", "quotapace pace off"} {
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
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("under pace must allow, got %q", out)
	}
}

func TestToleranceBoundary(t *testing.T) {
	e := newEnv(t)
	// 7d at 31 % used with 120 h left: sustainable 69/120 = 0.575 %/h, so the
	// refusal starts above 0.6325 %/h.
	f := fixture{account: "Work", used5: 10, used7: 31}
	e.stamp(t, f)
	e.burn(t, 0, 0.60, f)
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("within tolerance must allow, got %q", out)
	}
	os.Remove(filepath.Join(e.dir, "history.json"))
	e.burn(t, 0, 0.70, f)
	denied(t, e.hook(t, opusSpawn("x")))
}

// TestBurstThenQuietIsAllowedAgain reproduces the live defect: the cumulative
// average stays at twice the sustainable rate for days after a burst, but the
// fleet is quiet now, so the spawn must pass; and it is refused while the burst
// lasts.
func TestBurstThenQuietIsAllowedAgain(t *testing.T) {
	e := newEnv(t)
	f := fixture{account: "Work", used5: 30, used7: 67}
	e.stamp(t, f)
	e.burn(t, 0, 0, f) // the counter has not moved for half an hour
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("quiet now must allow, got %q", out)
	}
	rep := e.hook(t, "", "pace", "--json")
	var r Report
	if err := json.Unmarshal([]byte(rep), &r); err != nil {
		t.Fatal(err)
	}
	w := r.Accounts[0].Windows[1]
	if w.RatePerHour <= 2*w.SustainablePerHour || w.RecentPerHour == nil || *w.RecentPerHour != 0 || w.State != stateOK {
		t.Fatalf("cumulative must read high while recent reads 0: %+v", w)
	}
	os.Remove(filepath.Join(e.dir, "history.json"))
	e.burn(t, 0, 3, f) // 3 %/h for the last half hour
	denied(t, e.hook(t, opusSpawn("x")))
}

func TestNoRecentRateAllows(t *testing.T) {
	e := newEnv(t)
	f := fixture{account: "Work", used5: 30, used7: 90}
	e.stamp(t, f) // a hot cumulative average, no earlier reading
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("one reading is no rate, got %q", out)
	}
	if out := e.hook(t, "", "pace"); !strings.Contains(out, "recent unknown") || !strings.Contains(out, "[no_recent]") {
		t.Fatalf("report: %s", out)
	}
	// The reading just recorded is the history of the next one: 5 minutes on
	// with 3 more percent (36 %/h) that is a rate.
	now = func() time.Time { return t0.Add(5 * time.Minute) }
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), now(), fixture{account: "Work", used5: 30, used7: 93})
	denied(t, e.hook(t, opusSpawn("x"), "--max-age", "1h"))
}

func TestFiveHourWindowDenies(t *testing.T) {
	e := newEnv(t)
	f := fixture{account: "Work", used5: 69, used7: 10}
	e.stamp(t, f)
	e.burn(t, 20, 0, f)
	reason := denied(t, e.hook(t, opusSpawn("x")))
	if !strings.Contains(reason, "5h: used 69.0%") || strings.Contains(reason, "7d:") {
		t.Fatalf("reason: %s", reason)
	}
}

func TestExhaustedDenies(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, fixture{account: "Work", used5: 100, used7: 10, verdict: stamp.Exhausted})
	if reason := denied(t, e.hook(t, opusSpawn("x"))); !strings.Contains(reason, "exhausted") {
		t.Fatal(reason)
	}
}

// advised parses a hook output that allows with a note: no decision, an
// additionalContext.
func advised(t *testing.T, out string) string {
	t.Helper()
	var raw map[string]map[string]string
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("not an advisory JSON: %q: %v", out, err)
	}
	h := raw["hookSpecificOutput"]
	if h["hookEventName"] != "PreToolUse" || h["additionalContext"] == "" || h["permissionDecision"] != "" {
		t.Fatalf("bad advisory: %q", out)
	}
	return h["additionalContext"]
}

// A delegation cheaper than doing the work in the (opus) main session is never
// refused: over pace it passes with an advisory.
func TestCheaperDelegationsPassWithAdvice(t *testing.T) {
	e := newEnv(t)
	e.hot(t, over)
	for _, in := range []string{spawn("checker", "", "x"), spawn("general-purpose", "haiku", "x"), spawn("", "Haiku", "x"),
		spawn("implementer", "sonnet", "x"), spawn("implementer", "", "x"), spawn("reviewer", "sonnet", "x"), spawn("", "", "x")} {
		advice := advised(t, e.hook(t, in))
		for _, want := range []string{"at most 1 sub-agents running at once (of 3 usually)", "carry 2% of the recent burn", "recent 20.00%/h", "sustainable 0.32%/h", "Work (team plan)"} {
			if !strings.Contains(advice, want) {
				t.Errorf("%s: advice lacks %q:\n%s", in, want, advice)
			}
		}
	}
	// The concurrency is the usual one scaled by what the plan's windows carry.
	if !strings.Contains(advised(t, e.hook(t, spawn("implementer", "sonnet", "x"), "--max-agents", "200")), "at most 3 sub-agents running at once (of 200 usually)") {
		t.Error("--max-agents must scale with the plan's slowdown factor")
	}
	if out := e.hook(t, `{"tool_name":"Bash","tool_input":{"command":"ls"}}`); out != "" {
		t.Errorf("another tool must pass silently, got %q", out)
	}
}

func TestUnderPaceSonnetIsSilent(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, fine)
	if out := e.hook(t, spawn("implementer", "sonnet", "x")); out != "" {
		t.Fatalf("under pace: no note, got %q", out)
	}
}

func TestOpusIsRefusedOverPace(t *testing.T) {
	e := newEnv(t)
	e.hot(t, over)
	for _, in := range []string{spawn("reviewer", "opus", "x"), spawn("", "claude-opus-4-1", "x"), spawn("reviewer", "", "x"), spawn("implementer", "OPUS", "x")} {
		reason := denied(t, e.hook(t, in))
		for _, want := range []string{"opus sub-agent is refused", "model: sonnet", "pace-waiver:", "quotapace pace off"} {
			if !strings.Contains(reason, want) {
				t.Errorf("%s: reason lacks %q:\n%s", in, want, reason)
			}
		}
	}
}

func TestWaiverAllowsAndIsLogged(t *testing.T) {
	e := newEnv(t)
	e.hot(t, over)
	out := e.hook(t, opusSpawn("do it\n  pace-waiver: release blocked on this\nmore"))
	if out == "" || strings.Contains(out, "deny") || !strings.Contains(advised(t, out), "faster than it can last") {
		t.Fatalf("waiver must allow (with the note), got %q", out)
	}
	log, err := os.ReadFile(filepath.Join(e.dir, "waivers.log"))
	if err != nil || !strings.Contains(string(log), "release blocked on this") {
		t.Fatalf("waiver not logged: %q %v", log, err)
	}
	denied(t, e.hook(t, opusSpawn("pace-waiver:   "))) // an empty reason is no waiver
}

func TestOffOnSwitch(t *testing.T) {
	e := newEnv(t)
	e.hot(t, over)
	if out, _, _ := e.run(t, "", "pace", "off"); !strings.Contains(out, "off until `pace on`") {
		t.Fatalf("status after off: %q", out)
	}
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("off must allow silently, got %q", out)
	}
	e.run(t, "", "pace", "on")
	denied(t, e.hook(t, opusSpawn("x")))
}

func TestOffForExpires(t *testing.T) {
	e := newEnv(t)
	e.hot(t, over)
	out, _, _ := e.run(t, "", "pace", "off", "--for", "2h")
	if !strings.Contains(out, "off until") || strings.Contains(out, "pace on") {
		t.Fatalf("status must show the expiry: %q", out)
	}
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("inside the window must allow, got %q", out)
	}
	now = func() time.Time { return t0.Add(2*time.Hour + time.Second) }
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), now(), over)
	e.burnAt(t, now(), 20, 20, over)
	denied(t, e.hook(t, opusSpawn("x")))
	if out, _, _ := e.run(t, "", "pace", "status"); strings.Contains(out, "off") {
		t.Fatalf("an expired switch must read on: %q", out)
	}
}

func TestOffPerAccount(t *testing.T) {
	e := newEnv(t)
	e.hot(t, over, fixture{account: "Other", used5: 40, used7: 70})
	e.run(t, "", "pace", "off", "--account", "Work")
	if out := e.hook(t, opusSpawn("x"), "--account", "Work"); out != "" {
		t.Fatalf("Work is off, got %q", out)
	}
	reason := denied(t, e.hook(t, opusSpawn("x"), "--account", "Other"))
	if strings.Contains(reason, "Work") {
		t.Fatalf("Other's refusal must not name Work: %s", reason)
	}
	if out, _, _ := e.run(t, "", "pace", "status"); !strings.Contains(out, "account Work: off") || !strings.Contains(out, "gate: on") {
		t.Fatalf("status: %q", out)
	}
	e.run(t, "", "pace", "on", "--account", "Work")
	denied(t, e.hook(t, opusSpawn("x"), "--account", "Work"))
}

func TestAccountSelection(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over, fine)
	e.burn(t, 20, 20, over)
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("several stored accounts and none active: nothing decides, got %q", out)
	}
	denied(t, e.hook(t, opusSpawn("x"), "--account", "Work"))
	if out := e.hook(t, opusSpawn("x"), "--account", "Nobody"); out != "" {
		t.Fatalf("unknown account must allow, got %q", out)
	}
}

var (
	activeOver = fixture{account: "Claude Code", used5: 30, used7: 62}
	activeFine = fixture{account: "Claude Code", used5: 30, used7: 20}
)

// Only the account in use decides: another account's headroom does not rescue an
// over-pace active one, and another account's burn does not refuse a fine one.
func TestOnlyTheActiveAccountDecides(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, activeOver, fine)
	e.burn(t, 20, 20, activeOver)
	denied(t, e.hook(t, opusSpawn("x"))) // Home has headroom, the active account does not
	e.stamp(t, activeFine, over)
	e.burn(t, 20, 20, over)
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("the active account has headroom, got %q", out)
	}
}

// The active account changes during the day: the verdict follows the stamp's
// account and plan at every call, and the history of one plan never feeds another.
func TestSwitchingTheActivePlanFlipsTheWindows(t *testing.T) {
	e := newEnv(t)
	team := fixture{account: "Claude Code", used5: 30, used7: 62}
	e.hot(t, team)
	denied(t, e.hook(t, opusSpawn("x")))
	// Now billed to a max plan: same label, other plan, fine usage, no history.
	max := fixture{account: "Claude Code", used5: 30, used7: 20, plan: "max"}
	e.stamp(t, max)
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("the max plan is fine and has no recent rate: must allow, got %q", out)
	}
	out := e.hook(t, "", "pace", "--json")
	var r Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Accounts) != 1 || r.Accounts[0].Plan != "max" || !r.Accounts[0].Active {
		t.Fatalf("report must show the max plan active: %s", out)
	}
	// Back on team: its own history still holds and refuses again.
	e.stamp(t, team)
	denied(t, e.hook(t, opusSpawn("x")))
}

func TestUnknownPlanAllows(t *testing.T) {
	e := newEnv(t)
	f := fixture{account: "Claude Code", used5: 30, used7: 62, plan: noPlan}
	e.hot(t, f)
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("an unknown plan has no known windows: must allow, got %q", out)
	}
}

// A window the plan lists without a value is no constraint; a model-specific
// weekly window judges only a spawn on that model.
func TestModelWindowGovernsOnlyItsModel(t *testing.T) {
	e := newEnv(t)
	// The general windows are fine; Weekly Opus is over pace.
	f := fixture{account: "Claude Code", used5: 30, used7: 20, opus7: 62}
	e.stamp(t, f)
	e.burn(t, 0, 0, f) // the general windows are flat; Weekly Opus burns 20 %/h
	reason := denied(t, e.hook(t, opusSpawn("x")))
	if !strings.Contains(reason, "7d opus: used 62.0%") || strings.Contains(reason, "7d: used 20") {
		t.Fatalf("the Weekly Opus window must be named alone:\n%s", reason)
	}
	for _, in := range []string{spawn("implementer", "sonnet", "x"), spawn("", "haiku", "x")} {
		if out := e.hook(t, in); out != "" {
			t.Fatalf("Weekly Opus must not judge %s, got %q", in, out)
		}
	}
	// Without the window (n/a, as on a team plan) the same burn on the others is fine.
	g := fixture{account: "Claude Code", used5: 30, used7: 20}
	e.stamp(t, g)
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("an n/a window is no constraint, got %q", out)
	}
}

func TestModelNameOfWindow(t *testing.T) {
	for _, c := range []struct{ label, name, model string }{
		{"Session (5hr)", "5h", ""}, {"Weekly (7 day)", "7d", ""}, {"Weekly Opus", "7d opus", "opus"},
		{"Weekly Fable", "7d fable", "fable"}, {"Something", "", ""}, {"Weekly ", "", ""},
	} {
		name, model, _ := kindOf(c.label)
		if name != c.name || model != c.model {
			t.Errorf("%q: got %q %q, want %q %q", c.label, name, model, c.name, c.model)
		}
	}
	if (hookInput{}).runsOn("") || (hookInput{}).runsOn("opus") {
		t.Error("an empty spawn runs on no model")
	}
	if accountKey("A", "") != "A" || accountKey("A", "max") != "A=max" {
		t.Errorf("account key: %q %q", accountKey("A", ""), accountKey("A", "max"))
	}
}

func TestRecentWithoutAnySampleIsUnknown(t *testing.T) {
	if r := (History{}).recent("A", "5h", 10, t0, t0, time.Hour, time.Minute); r != nil {
		t.Fatalf("no sample, no rate: %v", *r)
	}
}

// The history is replaced whole or not at all, and a failure is an error.
func TestWriteHistoryErrors(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "sub", "h.json")
	if err := writeHistory(good, History{Samples: []Sample{{Account: "A"}}}); err != nil {
		t.Fatal(err)
	}
	if h := readHistory(good); len(h.Samples) != 1 {
		t.Fatalf("round trip: %+v", h)
	}
	// The directory of the file is a regular file: nothing can be created in it.
	if err := writeHistory(filepath.Join(good, "x.json"), History{}); err == nil {
		t.Fatal("an unwritable location is an error")
	}
	// The target is a non-empty directory: the rename fails and no temp file stays.
	blocked := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(filepath.Join(blocked, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeHistory(blocked, History{}); err == nil {
		t.Fatal("a failed rename is an error")
	}
	if _, err := os.Stat(blocked + ".tmp"); err == nil {
		t.Fatal("a failed write must not leave its temp file")
	}
}

func TestUnreadableInputsAllow(t *testing.T) {
	e := newEnv(t)
	e.hot(t, over)
	if err := os.WriteFile(filepath.Join(e.dir, "gate.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("a broken switch file must allow, got %q", out)
	}
	if _, _, code := e.run(t, "not json"); code == 0 {
		t.Fatal("bad hook JSON is a (non-blocking) error")
	}
}

func TestReportJSONSchema(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over, fine)
	e.burn(t, 20, 20, over)
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
	if len(accts) != 2 || r["schema"].(float64) != 2 {
		t.Fatalf("report: %s", out)
	}
	w := accts[0].(map[string]any)["windows"].([]any)[1].(map[string]any)
	for _, k := range []string{"window", "used_percent", "elapsed_percent", "recent_per_hour", "rate_per_hour", "sustainable_per_hour", "projected_at_reset_percent", "projected_exhaustion", "resets_at", "state"} {
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
	e.hot(t, over)
	e.run(t, "", "pace", "off", "--for", "2h")
	out := e.hook(t, "", "pace")
	for _, want := range []string{"gate: off until", "Work, plan team, ACTIVE (gate: on)", "used  62.0%", "[over_pace]", "aggregate"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "`pace on`") {
		t.Errorf("a timed off must show its expiry, not `pace on`:\n%s", out)
	}
	if _, _, code := newEnv(t).run(t, "", "pace"); code == 0 {
		t.Error("a report with no stamp is an error")
	}
}

func TestComputeTable(t *testing.T) {
	end := t0.Add(120 * time.Hour)
	rate := func(r float64) *float64 { return &r }
	if p := compute("7d", 168*time.Hour, 5, end, t0, 0.05, 0.1, rate(0.5)); p.State != stateOK || p.RatePerHour < 0.1 {
		t.Errorf("ok: %+v", p)
	}
	if p := compute("7d", 168*time.Hour, 5, t0.Add(167*time.Hour), t0, 0.05, 0.1, rate(50)); p.State != stateTooEarly || p.ProjectedExhaustion != nil {
		t.Errorf("too early: %+v", p)
	}
	p := compute("7d", 168*time.Hour, 62, end, t0, 0.05, 0.1, rate(5))
	if p.State != stateOverPace || p.ProjectedExhaustion == nil || !p.ProjectedExhaustion.Before(end) {
		t.Errorf("over: %+v", p)
	}
	if p := compute("7d", 168*time.Hour, 62, end, t0, 0.05, 0.1, nil); p.State != stateNoRecent || p.ProjectedExhaustion != nil {
		t.Errorf("no recent rate: %+v", p)
	}
	if p := compute("5h", 5*time.Hour, 50, t0.Add(-time.Minute), t0, 0.05, 0.1, rate(90)); p.State != stateOK || p.SustainablePerHour != 0 {
		t.Errorf("past reset: %+v", p)
	}
}

func TestRecentSlope(t *testing.T) {
	resets := t0.Add(48 * time.Hour)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	h := History{Samples: []Sample{
		{At: at(-90), Account: "A", Window: "7d", Used: 10, ResetsAt: resets}, // outside the hour
		{At: at(-50), Account: "A", Window: "7d", Used: 20, ResetsAt: resets}, // oldest inside
		{At: at(-20), Account: "A", Window: "7d", Used: 22, ResetsAt: resets},
		{At: at(-40), Account: "B", Window: "7d", Used: 0, ResetsAt: resets},                          // another account
		{At: at(-40), Account: "A", Window: "5h", Used: 0, ResetsAt: resets},                          // another window
		{At: at(-45), Account: "A", Window: "7d", Used: 99, ResetsAt: resets},                         // counter went back: skipped
		{At: at(-55), Account: "A", Window: "7d", Used: 0, ResetsAt: resets.Add(-7 * 24 * time.Hour)}, // previous instance
	}}
	r := h.recent("A", "7d", 25, resets, t0, time.Hour, 5*time.Minute)
	if r == nil || *r < 5.99 || *r > 6.01 { // (25-20) in 50 min
		t.Fatalf("slope: %v", r)
	}
	if h.recent("A", "7d", 25, resets, t0, 15*time.Minute, 5*time.Minute) != nil {
		t.Error("no sample inside a 15 minute lookback: unknown")
	}
	if h.recent("A", "7d", 22, resets, at(-20), time.Hour, 5*time.Minute) == nil || h.recent("A", "7d", 22, resets, at(-91), time.Hour, 5*time.Minute) != nil {
		t.Error("only samples strictly before the reading count")
	}
	if h.recent("A", "7d", 25, resets, at(-19), time.Hour, 5*time.Minute) == nil {
		t.Error("a 31 minute span counts")
	}
	if h.recent("A", "7d", 25, resets, at(-18), 40*time.Minute, 33*time.Minute) != nil {
		t.Error("a span shorter than --min-span is no rate")
	}
}

func TestHistoryAddPrunesAndDedups(t *testing.T) {
	var h History
	s := Sample{At: t0, Account: "A", Window: "5h", Used: 1, ResetsAt: t0.Add(time.Hour)}
	h, changed := h.add([]Sample{s})
	if !changed || len(h.Samples) != 1 {
		t.Fatalf("first add: %+v", h)
	}
	if h2, changed := h.add([]Sample{s}); changed || len(h2.Samples) != 1 {
		t.Fatal("the same reading twice must not grow the history")
	}
	old := s
	old.At = t0.Add(-time.Minute)
	if _, changed := h.add([]Sample{old}); changed {
		t.Fatal("an older reading than the newest held is not added")
	}
	late := s
	late.At = t0.Add(25 * time.Hour)
	if h, _ = h.add([]Sample{late}); len(h.Samples) != 1 || !h.Samples[0].At.Equal(late.At) {
		t.Fatalf("a sample older than 24 h must be pruned: %+v", h)
	}
	var big []Sample
	for i := 0; i < historyMax+10; i++ {
		x := late
		x.Window = "w" + string(rune('a'+i%2))
		x.At = late.At.Add(time.Duration(i+1) * time.Second)
		x.Account = "B" + string(rune('A'+i%20))
		big = append(big, x)
	}
	if h, _ = h.add(big); len(h.Samples) != historyMax {
		t.Fatalf("cap: %d", len(h.Samples))
	}
}

func fresh(t *testing.T, e env, f fixture) {
	t.Helper()
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), now(), f)
}

func TestStaleStampLockFreeRefreshes(t *testing.T) {
	e := newEnv(t)
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), t0.Add(-time.Hour), over)
	e.burn(t, 20, 20, over)
	var args []string
	startRefresh = func(_ string, a []string, _ time.Duration) error {
		refreshCalls++
		args = a
		fresh(t, e, over)
		return nil
	}
	denied(t, e.hook(t, opusSpawn("x")))
	if refreshCalls != 1 || len(args) != 3 || args[0] != "status" || args[1] != "--stamp" || args[2] != filepath.Join(e.dir, "stamp.json") {
		t.Fatalf("calls %d args %v", refreshCalls, args)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "refresh.json.lock")); err == nil {
		t.Error("the lock must be released")
	}
}

func TestMissingStampRefreshes(t *testing.T) {
	e := newEnv(t)
	e.hook(t, opusSpawn("x"))
	if refreshCalls != 1 {
		t.Fatalf("calls %d", refreshCalls)
	}
}

func TestFreshStampDoesNotRefresh(t *testing.T) {
	e := newEnv(t)
	e.hot(t, over)
	denied(t, e.hook(t, opusSpawn("x")))
	if refreshCalls != 0 {
		t.Fatalf("a fresh stamp needs no query, calls %d", refreshCalls)
	}
}

func TestCheapAndOffNeverRefresh(t *testing.T) {
	e := newEnv(t)
	e.run(t, "", "pace", "off")
	e.hook(t, opusSpawn("x"))
	if refreshCalls != 0 {
		t.Fatalf("calls %d", refreshCalls)
	}
}

func TestStaleStampLockHeldNoSecondQuery(t *testing.T) {
	e := newEnv(t)
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), t0.Add(-time.Hour), over)
	lock := filepath.Join(e.dir, "refresh.json.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := e.hook(t, opusSpawn("x")); out != "" {
		t.Fatalf("a stamp that cannot be refreshed allows, got %q", out)
	}
	if refreshCalls != 0 {
		t.Fatalf("another process holds the lock: no second query, calls %d", refreshCalls)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("a lock this process did not take must be left alone")
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	e.hook(t, opusSpawn("x"))
	if refreshCalls != 1 {
		t.Fatalf("a crashed holder's lock is reclaimed, calls %d", refreshCalls)
	}
}

func TestRefreshFailureAllowsAndWaitsBeforeRetry(t *testing.T) {
	e := newEnv(t)
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), t0.Add(-time.Hour), over)
	_, errw, _ := e.run(t, opusSpawn("x"))
	if refreshCalls != 1 || !strings.Contains(errw, "no refresher in tests") {
		t.Fatalf("calls %d stderr %q", refreshCalls, errw)
	}
	for _, m := range []int{1, 4} {
		now = func() time.Time { return t0.Add(time.Duration(m)*time.Minute + 59*time.Second) }
		if out := e.hook(t, opusSpawn("x")); out != "" {
			t.Fatalf("allow, got %q", out)
		}
	}
	if refreshCalls != 1 {
		t.Fatalf("a failed refresh is not retried inside --refresh-every, calls %d", refreshCalls)
	}
	now = func() time.Time { return t0.Add(5 * time.Minute) }
	e.hook(t, opusSpawn("x"))
	if refreshCalls != 2 {
		t.Fatalf("retry at exactly --refresh-every, calls %d", refreshCalls)
	}
	e.hook(t, opusSpawn("x"), "--refresh-every", "0")
	now = func() time.Time { return t0.Add(time.Hour) }
	e.hook(t, opusSpawn("x"), "--refresh-every", "0")
	if refreshCalls != 2 {
		t.Fatalf("--refresh-every 0 disables the refresh, calls %d", refreshCalls)
	}
}

func TestClockGoingBackDoesNotBlockRefresh(t *testing.T) {
	e := newEnv(t)
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), t0.Add(-time.Hour), over)
	e.hook(t, opusSpawn("x"))
	now = func() time.Time { return t0.Add(-3 * time.Hour) }
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), t0.Add(-4*time.Hour), over)
	e.hook(t, opusSpawn("x"))
	if refreshCalls != 2 {
		t.Fatalf("calls %d", refreshCalls)
	}
}

func TestRefreshWithoutARecordableAttemptDoesNotQuery(t *testing.T) {
	e := newEnv(t)
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), t0.Add(-time.Hour), over)
	if err := os.Mkdir(filepath.Join(e.dir, "refresh.json"), 0o700); err != nil { // a directory where the state file goes
		t.Fatal(err)
	}
	_, errw, _ := e.run(t, opusSpawn("x"))
	if refreshCalls != 0 || !strings.Contains(errw, "cannot record the attempt") {
		t.Fatalf("calls %d stderr %q", refreshCalls, errw)
	}
}

func TestHelperRefresher(t *testing.T) {
	switch os.Getenv("QUOTAPACE_HELPER") {
	case "slow":
		time.Sleep(3 * time.Second)
		os.Exit(0)
	case "status3":
		os.Exit(3)
	}
}

func TestExecRefresh(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUOTAPACE_HELPER", "slow")
	start := time.Now()
	err = execRefresh(exe, []string{"-test.run=^TestHelperRefresher$"}, 150*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "left to finish") || time.Since(start) > 2*time.Second {
		t.Fatalf("a slow refresher must be abandoned after the timeout: %v after %s", err, time.Since(start))
	}
	t.Setenv("QUOTAPACE_HELPER", "status3")
	if err := execRefresh(exe, []string{"-test.run=^TestHelperRefresher$"}, 30*time.Second); err != nil {
		t.Fatalf("aiquota status exits 3 for an unknown quota but still writes the stamp: %v", err)
	}
	t.Setenv("QUOTAPACE_HELPER", "")
	if err := execRefresh(filepath.Join(t.TempDir(), "absent"), nil, time.Second); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("a missing program is an error naming it: %v", err)
	}
	if err := execRefresh(t.TempDir(), nil, time.Second); err == nil {
		t.Fatal("a program that cannot start is an error")
	}
}

func TestFindProgramDefaultsToAiquotaBesideOrOnPath(t *testing.T) {
	t.Setenv("PATH", "")
	if _, err := findProgram("", t.TempDir()); err == nil {
		t.Fatal("no aiquota anywhere: an error")
	}
	beside := t.TempDir()
	name := filepath.Join(beside, "aiquota")
	if runtime.GOOS == "windows" {
		name += ".exe" // an executable needs its extension to be found
	}
	if err := os.WriteFile(name, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if p, err := findProgram("", beside); err != nil || p != name {
		t.Fatalf("aiquota beside the executable: %q %v", p, err)
	}
}

// A lock is honoured for less than lockTTL and reclaimed from exactly lockTTL.
func TestLockAgeBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	rel, ok := lockFile(path)
	if !ok {
		t.Fatal("a free lock must be taken")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	old := lockClock
	t.Cleanup(func() { lockClock = old })
	lockClock = func() time.Time { return fi.ModTime().Add(lockTTL - time.Nanosecond) }
	if _, ok := lockFile(path); ok {
		t.Fatal("a lock younger than lockTTL is held")
	}
	lockClock = func() time.Time { return fi.ModTime().Add(lockTTL) }
	rel2, ok := lockFile(path)
	if !ok {
		t.Fatal("a lock of lockTTL is reclaimed")
	}
	rel()
	rel2()
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
	e.burnAt(t, t0.Add(-15*time.Minute), 20, 20, over)
	denied(t, e.hook(t, opusSpawn("x"))) // age == --max-age: still fresh
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

// runWith runs with exactly these args (no default flags) and returns
// stdout, stderr and the exit code.
func runWith(stdin string, args ...string) (string, string, int) {
	var out, errw bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errw)
	return out.String(), errw.String(), code
}

func noConfigDir(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "AppData"} {
		t.Setenv(k, "")
	}
}

func TestUnresolvedConfigDir(t *testing.T) {
	e := newEnv(t)
	e.hot(t, over)
	noConfigDir(t)
	if _, errw, code := runWith("", "pace", "status"); code == 0 {
		t.Error("a command with no resolvable config dir must refuse")
	} else if !strings.Contains(errw, "HOME") && !strings.Contains(errw, "AppData") {
		t.Errorf("the refusal must say why: %q", errw)
	}
	// Every file given by flag: no config dir is needed.
	if _, errw, code := runWith("", append([]string{"pace", "status"}, e.flags()...)...); code != 0 {
		t.Errorf("all paths given, yet refused: %q", errw)
	}
	// The hook has nothing to read, so it allows.
	if out, _, _ := runWith(opusSpawn("x")); out != "" {
		t.Errorf("no readable files must allow, got %q", out)
	}
	// With the stamp and the switches given but no log: still judged, the waiver
	// passes and its log failure is said on stderr.
	args := []string{"--stamp", filepath.Join(e.dir, "stamp.json"), "--gate", filepath.Join(e.dir, "gate.json"), "--history", filepath.Join(e.dir, "history.json"), "--refresh-every", "0"}
	denied(t, func() string { o, _, _ := runWith(opusSpawn("x"), args...); return o }())
	out, errw, code := runWith(opusSpawn("pace-waiver: because"), args...)
	if strings.Contains(out, "permissionDecision") || code != 0 || !strings.Contains(errw, "waiver not logged") {
		t.Errorf("waiver without a log path: out=%q code=%d err=%q", out, code, errw)
	}
}

func TestWaiverLogFailuresAreSaidAndNeverRefuse(t *testing.T) {
	e := newEnv(t)
	e.hot(t, over)
	blocker := filepath.Join(e.dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	readonly := filepath.Join(e.dir, "ro.log")
	if err := os.WriteFile(readonly, nil, 0o400); err != nil {
		t.Fatal(err)
	}
	base := []string{"--stamp", filepath.Join(e.dir, "stamp.json"), "--gate", filepath.Join(e.dir, "gate.json"), "--history", filepath.Join(e.dir, "history.json")}
	for _, c := range []struct{ log, stage string }{{filepath.Join(blocker, "w.log"), "creating"}, {readonly, "opening"}} {
		if c.stage == "opening" && os.Geteuid() == 0 {
			continue // root opens a read-only file
		}
		out, errw, code := runWith(opusSpawn("pace-waiver: r"), append(base, "--waiver-log", c.log)...)
		if strings.Contains(out, "permissionDecision") || code != 0 || !strings.Contains(errw, "waiver not logged") || !strings.Contains(errw, c.stage) {
			t.Errorf("%s: out=%q code=%d err=%q", c.stage, out, code, errw)
		}
	}
	good := filepath.Join(e.dir, "sub", "ok.log")
	if out, errw, _ := runWith(opusSpawn("pace-waiver: r"), append(base, "--waiver-log", good)...); strings.Contains(out, "permissionDecision") || errw != "" {
		t.Errorf("a logged waiver says nothing: out=%q err=%q", out, errw)
	}
}

func TestSwitchFileErrorStages(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over)
	gate := filepath.Join(e.dir, "gate.json")
	if err := os.Mkdir(gate, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, errw, code := e.run(t, "", "pace", "status"); code == 0 || !strings.Contains(errw, "reading") {
		t.Errorf("a directory as the switch file: code=%d err=%q", code, errw)
	}
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gate, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errw, code := e.run(t, "", "pace", "status"); code == 0 || !strings.Contains(errw, "parsing") {
		t.Errorf("a broken switch file: code=%d err=%q", code, errw)
	}
	blocker := filepath.Join(e.dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errw, code := runWith("", "pace", "off", "--gate", filepath.Join(blocker, "g.json")); code == 0 || !(strings.Contains(errw, "reading") || strings.Contains(errw, "creating")) {
		t.Errorf("a file as the directory: code=%d err=%q", code, errw) // the stage differs by OS
	}
	if os.Geteuid() != 0 && runtime.GOOS != "windows" { // a 0500 directory does not block on Windows
		locked := filepath.Join(e.dir, "locked")
		if err := os.Mkdir(locked, 0o500); err != nil {
			t.Fatal(err)
		}
		if _, errw, code := runWith("", "pace", "off", "--gate", filepath.Join(locked, "sub", "g.json")); code == 0 || !strings.Contains(errw, "creating") {
			t.Errorf("an unwritable directory: code=%d err=%q", code, errw)
		}
		ro := filepath.Join(e.dir, "ro.json")
		if err := os.WriteFile(ro, []byte("{}"), 0o400); err != nil {
			t.Fatal(err)
		}
		if _, errw, code := runWith("", "pace", "off", "--gate", ro); code == 0 || !strings.Contains(errw, "opening") {
			t.Errorf("a read-only switch file: code=%d err=%q", code, errw)
		}
	}
}
