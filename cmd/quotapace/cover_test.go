// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-aiquota/tray/stamp"
)

// Every default under os.UserConfigDir()/aiquota, one at a time: the others are
// given by flag, so only the one under test is resolved.
func TestResolvePathsDefaultsEachPathOnItsOwn(t *testing.T) {
	newEnv(t) // the config dir is a temp dir
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := stamp.DefaultPath()
	cases := []struct {
		name string
		in   paths
		get  func(paths) string
		want string
	}{
		{"stamp", paths{"", "g", "w", "h", "r"}, func(p paths) string { return p.stamp }, sp},
		{"gate", paths{"s", "", "w", "h", "r"}, func(p paths) string { return p.gate }, filepath.Join(cfg, "aiquota", "pace-gate.json")},
		{"waiver", paths{"s", "g", "", "h", "r"}, func(p paths) string { return p.waiver }, filepath.Join(cfg, "aiquota", "pace-waivers.log")},
		{"history", paths{"s", "g", "w", "", "r"}, func(p paths) string { return p.history }, filepath.Join(cfg, "aiquota", "pace-history.json")},
		{"refresh", paths{"s", "g", "w", "h", ""}, func(p paths) string { return p.refresh }, filepath.Join(cfg, "aiquota", "pace-refresh.json")},
	}
	for _, c := range cases {
		p, err := resolvePaths(c.in.stamp, c.in.gate, c.in.waiver, c.in.history, c.in.refresh)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := c.get(p); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		// Every path given by flag is returned untouched.
		for i, pair := range [][2]string{{c.in.stamp, p.stamp}, {c.in.gate, p.gate}, {c.in.waiver, p.waiver}, {c.in.history, p.history}, {c.in.refresh, p.refresh}} {
			if pair[0] != "" && pair[0] != pair[1] {
				t.Errorf("%s: path %d given as %q came back %q", c.name, i, pair[0], pair[1])
			}
		}
	}
}

func TestComputeClampsALongWayToTheReset(t *testing.T) {
	// A reset further off than the window is long (a clock skew, a reading of
	// the next window): the window counts as not begun, never negative elapsed.
	p := compute("5h", 5*time.Hour, 10, t0.Add(8*time.Hour), t0, 0.05, 0.1, nil)
	if p.ElapsedPercent != 0 || p.RatePerHour != 0 || p.ProjectedAtReset != 0 {
		t.Errorf("elapsed %v rate %v projected %v, want 0", p.ElapsedPercent, p.RatePerHour, p.ProjectedAtReset)
	}
	if want := 90.0 / 5; p.SustainablePerHour != want {
		t.Errorf("sustainable %v, want %v (what is left over the window length, not over the distance)", p.SustainablePerHour, want)
	}
	if p.State != stateTooEarly {
		t.Errorf("state %s", p.State)
	}
	// Exactly the window length is not clamped to anything else.
	if q := compute("5h", 5*time.Hour, 10, t0.Add(5*time.Hour), t0, 0.05, 0.1, nil); q.SustainablePerHour != p.SustainablePerHour || q.ElapsedPercent != 0 {
		t.Errorf("at the window length: %+v", q)
	}
}

func TestComputeNeverSustainsANegativeRate(t *testing.T) {
	// More than 100 % used (a counter that ran past its limit): nothing is left,
	// so the sustainable rate is 0, not negative.
	p := compute("5h", 5*time.Hour, 120, t0.Add(2*time.Hour), t0, 0.05, 0.1, nil)
	if p.SustainablePerHour != 0 || p.State != stateExhausted {
		t.Errorf("sustainable %v state %s", p.SustainablePerHour, p.State)
	}
	// Exactly 100 % used leaves 0 as well, by the same arithmetic.
	if p := compute("5h", 5*time.Hour, 100, t0.Add(2*time.Hour), t0, 0.05, 0.1, nil); p.SustainablePerHour != 0 {
		t.Errorf("at 100%%: %v", p.SustainablePerHour)
	}
}

func TestReadingsDerivePercentFromUsedAndLimit(t *testing.T) {
	win := func(used, limit, pct float64) stamp.Window {
		return stamp.Window{Account: "A", Plan: "team", Label: "Session (5hr)", Used: used, Limit: limit, UsedPercent: pct,
			ResetsAtUnix: t0.Add(time.Hour).Unix(), Verdict: stamp.Available}
	}
	cases := []struct {
		name string
		w    stamp.Window
		want float64
	}{
		{"percent given", win(30, 100, 40), 40},
		{"percent absent: used over limit", win(30, 120, 0), 25},
		{"percent absent, no limit", win(30, 0, 0), 0},
		{"nothing used", win(0, 100, 0), 0},
	}
	for _, c := range cases {
		rs := readings(stamp.Stamp{Windows: []stamp.Window{c.w}})
		if len(rs) != 1 || rs[0].used != c.want {
			t.Errorf("%s: %+v, want used %v", c.name, rs, c.want)
		}
	}
}

func TestAggregateSkipsAWindowKindNoAccountHas(t *testing.T) {
	as := []AccountPace{{Account: "A", Plan: "team", Windows: []Pace{{Window: "5h", UsedPercent: 20, ResetsAt: t0.Add(time.Hour)}}}}
	got := aggregate(as, t0, 0.05, 0.1)
	if len(got) != 1 || got[0].Window != "5h" {
		t.Fatalf("got %+v, want only the 5h window", got)
	}
	if got := aggregate(nil, t0, 0.05, 0.1); len(got) != 0 {
		t.Errorf("no accounts: %+v", got)
	}
	as[0].Windows = []Pace{{Window: "7d", UsedPercent: 20, ResetsAt: t0.Add(time.Hour)}}
	if got := aggregate(as, t0, 0.05, 0.1); len(got) != 1 || got[0].Window != "7d" {
		t.Errorf("got %+v, want only the 7d window", got)
	}
}

func TestReportWithNoPacedWindowHasAnEmptyAggregate(t *testing.T) {
	e := newEnv(t)
	st := stamp.Stamp{Version: stamp.Version, At: t0, Verdict: stamp.Available,
		Windows: []stamp.Window{{Account: "A", Plan: "team", Label: "Extra usage", Verdict: stamp.Available}}}
	if err := stamp.Write(filepath.Join(e.dir, "stamp.json"), st); err != nil {
		t.Fatal(err)
	}
	var r struct {
		Accounts  []json.RawMessage `json:"accounts"`
		Aggregate []json.RawMessage `json:"aggregate"`
	}
	out := e.hook(t, "", "pace", "--json")
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Accounts == nil || r.Aggregate == nil || len(r.Aggregate) != 0 {
		t.Errorf("accounts and aggregate must be [] not null: %s (%v)", out, err)
	}
	if !strings.Contains(out, `"aggregate": []`) && !strings.Contains(out, `"aggregate":[]`) {
		t.Errorf("aggregate is not an empty array: %s", out)
	}
}

func TestReportTextStaleAndAggregateLabels(t *testing.T) {
	e := newEnv(t)
	e.stamp(t, over, fine)
	out := e.hook(t, "", "pace")
	for _, want := range []string{"(fresh)", "aggregate (mean of the accounts)\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("two accounts, fresh stamp: lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "STALE") {
		t.Errorf("a fresh stamp reported stale:\n%s", out)
	}

	e = newEnv(t)
	writeStamp(t, filepath.Join(e.dir, "stamp.json"), t0.Add(-time.Hour), over)
	out = e.hook(t, "", "pace")
	if !strings.Contains(out, "(STALE, the hook allows)") || strings.Contains(out, "(fresh)") {
		t.Errorf("a stale stamp is not reported so:\n%s", out)
	}
	if !strings.Contains(out, "aggregate\n") || strings.Contains(out, "mean of the accounts") {
		t.Errorf("one account: the aggregate is not a mean:\n%s", out)
	}
}

func TestHistoryNotSavedIsSaidAndNeverFatal(t *testing.T) {
	e := newEnv(t)
	e.hot(t, over)
	blocker := filepath.Join(e.dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The last --history wins: its directory is a regular file.
	args := append(e.flags(), "--history", filepath.Join(blocker, "sub", "h.json"))
	var out, errw bytes.Buffer
	code := run(args, strings.NewReader(opusSpawn("x")), &out, &errw)
	if code != 0 || !strings.Contains(errw.String(), "quotapace: history not saved:") {
		t.Errorf("code %d err %q", code, errw.String())
	}
	// Without the history there is no recent slope: nothing refuses.
	if strings.Contains(out.String(), "deny") {
		t.Errorf("refused without a history: %s", out.String())
	}
}

func TestExecRefreshProgramThatCannotRun(t *testing.T) {
	// Found by the lookup, but not an executable format: the start fails with
	// an error that is not an exit status, and it is returned.
	name := filepath.Join(t.TempDir(), "notaprogram")
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(name, []byte("this is not a program\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := execRefresh(name, nil, 10*time.Second); err == nil || strings.Contains(err.Error(), "left to finish") {
		t.Errorf("got %v, want the start error", err)
	}
}

func TestMainExitsWithRunsCode(t *testing.T) {
	oldExit, oldArgs, oldIn := osExit, os.Args, os.Stdin
	t.Cleanup(func() { osExit, os.Args, os.Stdin = oldExit, oldArgs, oldIn })
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	os.Stdin = in
	os.Args = []string{"quotapace", "--version"}
	code := -1
	osExit = func(c int) { code = c }
	main()
	if code != 0 {
		t.Errorf("exit code %d", code)
	}
	// An empty stdin is not hook JSON: a non-blocking error, exit 1.
	os.Args = []string{"quotapace"}
	main()
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
}
