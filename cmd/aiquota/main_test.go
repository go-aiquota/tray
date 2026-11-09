// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/go-aiquota/proto/quotapb"
	"github.com/go-aiquota/tray/account"
	"github.com/go-aiquota/tray/stamp"
)

type fake struct {
	accts []account.Account
	snaps map[string]*quotapb.QuotaSnapshot
	errs  map[string]error
	// cc / ccErr answer the virtual Claude Code account.
	cc    *quotapb.QuotaSnapshot
	ccErr error
}

func (f *fake) Accounts() ([]account.Account, error) { return f.accts, nil }
func (f *fake) Close()                               {}
func (f *fake) Fetch(_ context.Context, a account.Account) (*quotapb.QuotaSnapshot, error) {
	if a.ID == claudeCodeAccount.ID {
		return f.cc, f.ccErr
	}
	if e := f.errs[a.Label]; e != nil {
		return nil, e
	}
	return f.snaps[a.Label], nil
}

func win(label string, used float64, reset int64) *quotapb.QuotaWindow {
	return &quotapb.QuotaWindow{Label: label, Used: used, Limit: 100, Unit: "%", ResetsAtUnix: reset}
}

var t10 = time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

func runStatus(t *testing.T, f *fake, args ...string) (code int, out string, st stamp.Stamp) {
	t.Helper()
	oldLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = oldLocal })
	now = func() time.Time { return t10 }
	newBackend = func() (backend, error) { return f, nil }
	path := filepath.Join(t.TempDir(), "aiquota", "last-status.json")
	var o, e bytes.Buffer
	code = run(append(args, "--stamp", path), &o, &e)
	st, err := stamp.Read(path)
	if err != nil {
		t.Fatalf("no stamp written: %v (stderr %q)", err, e.String())
	}
	return code, o.String(), st
}

// one is a fake with a stored claude.ai account "max" holding snap.
func one(snap *quotapb.QuotaSnapshot) *fake {
	return &fake{
		accts: []account.Account{{ID: "a1", Provider: "claude", Label: "max"}},
		snaps: map[string]*quotapb.QuotaSnapshot{"max": snap},
		ccErr: status.Error(codes.NotFound, "no Claude Code login found"),
	}
}

func TestAvailable(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{PlanKind: "max", Windows: []*quotapb.QuotaWindow{win("session", 12, 1788000000), win("weekly_all", 40, 1788300000)}})
	code, out, st := runStatus(t, f, "status", "--account", "max")
	if code != 0 || st.Verdict != stamp.Available || len(st.Windows) != 2 {
		t.Fatalf("code %d stamp %+v", code, st)
	}
	for _, want := range []string{"Plan: max", " 12%", "weekly_all", "[AVAILABLE]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// The equality oracle's local half: the text is the Account & usage panel's
// rows (webview WDn / dle / Zy) line by line. The plugin test holds the other
// half (response -> windows).
func TestTextIsThePanelRowsLineByLine(t *testing.T) {
	f := one(nil)
	f.ccErr = nil
	f.cc = &quotapb.QuotaSnapshot{PlanKind: "max", Windows: []*quotapb.QuotaWindow{
		win("Session (5hr)", 37.9, time.Date(2026, 11, 9, 13, 0, 0, 0, time.UTC).Unix()),
		win("Weekly (7 day)", 52, time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC).Unix()),
		win("Weekly Sonnet", 8.5, time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC).Unix()),
		win("Weekly Fable", 100, time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC).Unix()),
	}}
	code, out, _ := runStatus(t, f, "status")
	if code != 1 {
		t.Fatalf("code %d (Fable window is at 100%%)", code)
	}
	want := []string{
		"Account: Claude Code",
		"Plan: max",
		"Session (5hr)     37%   Resets at 13:00   [AVAILABLE]",
		"Weekly (7 day)    52%   Resets Thu, Nov 12 at 02:00   [AVAILABLE]",
		"Weekly Sonnet      8%   Resets Thu, Nov 12 at 02:00   [AVAILABLE]",
		"Weekly Fable     100%   Resets Thu, Nov 12 at 02:00   [EXHAUSTED]",
	}
	lines := strings.Split(out, "\n")
	for i, w := range want {
		if lines[i+1] != w { // line 0 is "measured ..."
			t.Errorf("line %d:\n got %q\nwant %q", i+1, lines[i+1], w)
		}
	}
}

func TestClaudeCodeIsTheDefaultAndStoredAccountsAreNotMixedIn(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{win("session", 99, 0)}})
	f.ccErr = nil
	f.cc = &quotapb.QuotaSnapshot{PlanKind: "max", Windows: []*quotapb.QuotaWindow{win("Session (5hr)", 1, 0)}}
	_, _, st := runStatus(t, f, "status")
	if len(st.Windows) != 1 || st.Windows[0].Account != "Claude Code" {
		t.Fatalf("windows %+v", st.Windows)
	}
}

func TestStoredAccountIsTheFallbackWhenThereIsNoClaudeCodeLogin(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{PlanKind: "max", Windows: []*quotapb.QuotaWindow{win("session", 12, 0)}})
	code, _, st := runStatus(t, f, "status")
	if code != 0 || len(st.Windows) != 1 || st.Windows[0].Account != "max" || len(st.Problems) != 0 {
		t.Fatalf("code %d stamp %+v", code, st)
	}
}

func TestExpiredClaudeCodeTokenIsUnknownNotAFallback(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{win("session", 1, 0)}})
	f.ccErr = status.Error(codes.FailedPrecondition, "token expired, open Claude Code")
	code, out, st := runStatus(t, f, "status")
	if code != 3 || st.Verdict != stamp.Unknown || len(st.Windows) != 1 {
		t.Fatalf("code %d stamp %+v", code, st)
	}
	if !strings.Contains(out, "token expired, open Claude Code") || strings.Contains(out, "rpc error") {
		t.Errorf("output: %s", out)
	}
}

func TestExhaustedWinsOverAvailable(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{win("session", 5, 0), win("weekly_scoped", 100, 1788300000)}})
	code, _, st := runStatus(t, f, "status", "--account", "max")
	if code != 1 || st.Verdict != stamp.Exhausted {
		t.Fatalf("code %d verdict %s", code, st.Verdict)
	}
}

func TestFetchErrorIsUnknownNeverAvailable(t *testing.T) {
	f := one(nil)
	f.errs = map[string]error{"max": errors.New("cloudflare challenge")}
	code, out, st := runStatus(t, f, "status", "--account", "max")
	if code != 3 || st.Verdict != stamp.Unknown || len(st.Problems) == 0 {
		t.Fatalf("code %d stamp %+v", code, st)
	}
	if !strings.Contains(out, "UNKNOWN") {
		t.Errorf("output: %s", out)
	}
}

type hang struct{ *fake }

func (hang) Fetch(context.Context, account.Account) (*quotapb.QuotaSnapshot, error) {
	select {} // ignores its context, like a keyring prompt nobody answers
}

func TestHungBackendIsUnknownNotAHang(t *testing.T) {
	h := hang{one(nil)}
	now = func() time.Time { return t10 }
	newBackend = func() (backend, error) { return h, nil }
	path := filepath.Join(t.TempDir(), "s.json")
	var o, e bytes.Buffer
	if code := run([]string{"status", "--timeout", "50ms", "--stamp", path}, &o, &e); code != 3 {
		t.Fatalf("code %d, want 3\n%s", code, o.String())
	}
	if st, _ := stamp.Read(path); st.Verdict != stamp.Unknown {
		t.Fatalf("stamp %+v", st)
	}
}

func TestNoLimitIsUnknown(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{{Label: "x", Used: 1}}})
	if code, _, st := runStatus(t, f, "status", "--account", "max"); code != 3 || st.Verdict != stamp.Unknown {
		t.Fatalf("code %d verdict %s", code, st.Verdict)
	}
}

func TestNoAccountIsUnknown(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{win("session", 1, 0)}})
	if code, _, _ := runStatus(t, f, "status", "--account", "nobody"); code != 3 {
		t.Fatalf("code %d, want 3", code)
	}
}

func TestJSONOutputIsTheStamp(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{win("session", 1, 0)}})
	_, out, _ := runStatus(t, f, "status", "-o", "json", "--account", "max")
	if !strings.Contains(out, `"verdict": "available"`) {
		t.Errorf("json: %s", out)
	}
}

func statusbarOf(t *testing.T, f *fake, args ...string) (int, statusBar) {
	t.Helper()
	code, out, _ := runStatus(t, f, append([]string{"status", "-o", "statusbar"}, args...)...)
	var sb statusBar
	if err := json.Unmarshal([]byte(out), &sb); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, out)
	}
	return code, sb
}

func TestStatusbarShowsSessionAndWeeklyWithLevels(t *testing.T) {
	reset := time.Date(2026, 11, 9, 13, 0, 0, 0, time.UTC).Unix()
	for _, c := range []struct {
		session, weekly, model float64
		level                  string
		code                   int
	}{
		{30, 5, 0, levelOK, 0},
		{30, 5, 80, levelWarn, 0},
		{92, 5, 0, levelError, 0},
		{100, 5, 0, levelError, 1},
	} {
		f := one(nil)
		f.ccErr = nil
		f.cc = &quotapb.QuotaSnapshot{PlanKind: "team", Windows: []*quotapb.QuotaWindow{
			win("Session (5hr)", c.session, reset), win("Weekly (7 day)", c.weekly, reset), win("Weekly Fable", c.model, reset)}}
		code, sb := statusbarOf(t, f)
		if code != c.code || sb.Level != c.level || !strings.HasPrefix(sb.Text, "Claude ") {
			t.Errorf("%+v: code %d, %+v", c, code, sb)
		}
	}
	f := one(nil)
	f.ccErr = nil
	f.cc = &quotapb.QuotaSnapshot{PlanKind: "team", Windows: []*quotapb.QuotaWindow{win("Session (5hr)", 30, t10.Add(3*time.Hour).Unix()), win("Weekly (7 day)", 5, t10.Add(72*time.Hour).Unix())}}
	_, sb := statusbarOf(t, f)
	if sb.Text != "Claude 30% · 5%" {
		t.Errorf("text %q", sb.Text)
	}
	for _, want := range []string{"Claude Code (team)", "Session (5hr)  30%  resets at 13:00", "measured 2026-11-09 10:00:00 UTC"} {
		if !strings.Contains(sb.Tooltip, want) {
			t.Errorf("tooltip lacks %q:\n%s", want, sb.Tooltip)
		}
	}
	if _, sb = statusbarOf(t, f, "--warn", "20", "--critical", "40"); sb.Level != levelWarn {
		t.Errorf("custom thresholds: %+v", sb)
	}
}

func TestStatusbarUnknownIsNeverANumber(t *testing.T) {
	f := one(nil)
	f.ccErr = errors.New("token expired")
	code, sb := statusbarOf(t, f)
	if code != 3 || sb.Text != "Claude ?" || sb.Level != levelUnknown || !strings.Contains(sb.Tooltip, "token expired") ||
		!strings.Contains(sb.Tooltip, "measured 2026-11-09 10:00:00 UTC") || strings.ContainsAny(sb.Text, "0123456789") {
		t.Errorf("code %d, %+v", code, sb)
	}
}

func TestBadThresholdsAreUsageError(t *testing.T) {
	newBackend = func() (backend, error) { return one(nil), nil }
	if code := run([]string{"status", "-o", "statusbar", "--warn", "95", "--critical", "90"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("code %d, want 2", code)
	}
}

func TestBadFormatIsUsageError(t *testing.T) {
	f := one(nil)
	newBackend = func() (backend, error) { return f, nil }
	if code := run([]string{"status", "-o", "xml"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("code %d, want 2", code)
	}
}

func TestErrorsAreRedacted(t *testing.T) {
	f := one(nil)
	f.errs = map[string]error{"max": errors.New("boom sk-ant-sid01-abcdefghijklmnopqrstuvwxyz0123456789")}
	_, out, st := runStatus(t, f, "status", "--account", "max")
	if strings.Contains(out, "abcdefghijklmnop") || strings.Contains(st.Problems[0], "abcdefghijklmnop") {
		t.Errorf("secret leaked: %s / %v", out, st.Problems)
	}
}

func TestPanelResetWording(t *testing.T) {
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	oldLocal := time.Local
	time.Local = time.UTC
	defer func() { time.Local = oldLocal }()
	cases := []struct {
		reset time.Time
		label string
		want  string
	}{
		{at.Add(30 * time.Second), "Session (5hr)", "soon"},
		{at.Add(3 * time.Hour), "Session (5hr)", "at 13:00"},
		{at.Add(30 * time.Hour), "Session (5hr)", "Tue, Nov 10 at 16:00"},
		{at.Add(3 * time.Hour), "Weekly (7 day)", "Mon, Nov 9 at 13:00"},
		{time.Date(2027, 1, 4, 9, 5, 0, 0, time.UTC), "Weekly (7 day)", "Mon, Jan 4, 2027 at 09:05"},
	}
	for _, c := range cases {
		if got := panelReset(c.reset.Unix(), c.label, at); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.label, c.reset, got, c.want)
		}
	}
	for in, want := range map[float64]int{37.9: 37, 100: 100, 104: 100, -3: 0, 0: 0} {
		if got := panelPercent(in); got != want {
			t.Errorf("panelPercent(%v) = %d, want %d", in, got, want)
		}
	}
}
