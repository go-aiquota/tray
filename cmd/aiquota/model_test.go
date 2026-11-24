// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aiquota/proto/quotapb"
	"github.com/go-aiquota/tray/stamp"
)

var (
	r5h   = time.Date(2026, 11, 9, 13, 0, 0, 0, time.UTC).Unix()
	rWeek = time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC).Unix()
)

// teamFable is the shape measured on the team login 2026-11-24: session 29 %,
// weekly 8 %, a window scoped to Fable at 3 %, and Sonnet/Opus listed without a
// value (seven_day_sonnet and seven_day_opus null in the raw response).
func teamFable() *fake {
	f := one(nil)
	f.ccErr = nil
	f.cc = &quotapb.QuotaSnapshot{PlanKind: "team", Windows: []*quotapb.QuotaWindow{
		win("Session (5hr)", 29, r5h), win("Weekly (7 day)", 8, rWeek), win("Weekly Fable", 3, rWeek),
		{Label: "Weekly Sonnet", Unit: unitNotAvailable}, {Label: "Weekly Opus", Unit: unitNotAvailable},
	}}
	return f
}

func TestModelWithNoSeparateLimitIsAvailableNotUnknown(t *testing.T) {
	code, out, _ := runStatus(t, teamFable(), "status", "--model", "SoNNet")
	if code != 0 {
		t.Fatalf("code %d:\n%s", code, out)
	}
	for _, want := range []string{
		"measured 2026-11-09 10:00 UTC", "Account: Claude Code", "Plan: team",
		"no separate SoNNet limit on this plan",
		"Session (5hr)     29%   Resets at 13:00   [AVAILABLE]",
		"Weekly (7 day)     8%   Resets Thu, Nov 12 at 02:00   [AVAILABLE]",
		"AVAILABLE: no separate SoNNet limit on this plan; governed by the session and weekly limits",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Fable") || strings.Contains(out, "N/A") {
		t.Errorf("only the limits that apply, no N/A:\n%s", out)
	}
}

func TestModelWithAScopedLimitListsIt(t *testing.T) {
	code, out, _ := runStatus(t, teamFable(), "status", "--model", "fable")
	if code != 0 || !strings.Contains(out, "Weekly Fable       3%") || strings.Contains(out, "no separate") {
		t.Fatalf("code %d:\n%s", code, out)
	}
}

func TestModelScopedLimitAtOneHundredIsExhausted(t *testing.T) {
	f := one(nil)
	f.ccErr = nil
	f.cc = &quotapb.QuotaSnapshot{PlanKind: "max", Windows: []*quotapb.QuotaWindow{
		win("Session (5hr)", 10, r5h), win("Weekly (7 day)", 20, rWeek), win("Weekly Sonnet", 100, rWeek),
	}}
	code, out, _ := runStatus(t, f, "status", "--model", "sonnet")
	if code != 1 {
		t.Fatalf("code %d:\n%s", code, out)
	}
	if !strings.Contains(out, "EXHAUSTED: Weekly Sonnet exhausted (100%), resets Thu, Nov 12 at 02:00") {
		t.Errorf("exhausted line missing:\n%s", out)
	}
}

func TestModelSessionExhaustedBlocksEveryModel(t *testing.T) {
	f := teamFable()
	f.cc.Windows[0] = win("Session (5hr)", 100, 0)
	code, out, _ := runStatus(t, f, "status", "--model", "opus")
	if code != 1 || !strings.Contains(out, "Session (5hr) exhausted (100%), resets at an unknown time") {
		t.Fatalf("code %d:\n%s", code, out)
	}
}

func TestModelFailedReadIsUnknown(t *testing.T) {
	f := one(nil)
	f.ccErr = errors.New("boom")
	code, out, st := runStatus(t, f, "status", "--model", "sonnet")
	if code != 3 || st.Verdict != stamp.Unknown {
		t.Fatalf("code %d stamp %+v", code, st)
	}
	if !strings.Contains(out, "UNKNOWN: could not read the limits") || strings.Contains(out, "no separate") {
		t.Errorf("unknown must not read as no-limit:\n%s", out)
	}
}

func TestModelNoWindowsIsUnknown(t *testing.T) {
	f := one(nil)
	f.ccErr = nil
	f.cc = &quotapb.QuotaSnapshot{PlanKind: "team"}
	if code, out, _ := runStatus(t, f, "status", "--model", "sonnet"); code != 3 {
		t.Fatalf("code %d:\n%s", code, out)
	}
}

func TestModelOnlyUnlistedWindowsIsUnknown(t *testing.T) {
	// Session and weekly missing: nothing measured, so "no separate limit" is
	// not enough to call it available.
	f := one(nil)
	f.ccErr = nil
	f.cc = &quotapb.QuotaSnapshot{PlanKind: "team", Windows: []*quotapb.QuotaWindow{win("Weekly Fable", 3, rWeek)}}
	if code, out, _ := runStatus(t, f, "status", "--model", "sonnet"); code != 3 {
		t.Fatalf("code %d:\n%s", code, out)
	}
}

func TestModelJSONCarriesTheAnswer(t *testing.T) {
	code, out, _ := runStatus(t, teamFable(), "status", "--model", "sonnet", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d:\n%s", code, out)
	}
	var got struct {
		Verdict stamp.Verdict `json:"verdict"`
		Windows []stamp.Window
		Model   modelAnswer `json:"model"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	m := got.Model
	if m.Verdict != stamp.Available || m.Scoped || m.Plan != "team" || m.Account != "Claude Code" || len(m.Limits) != 2 || len(got.Windows) != 5 {
		t.Errorf("answer %+v", m)
	}
}

func TestModelRefusedWithStatusbar(t *testing.T) {
	var o, e strings.Builder
	if code := run([]string{"status", "--model", "sonnet", "-o", "statusbar"}, &o, &e); code != 2 || !strings.Contains(e.String(), "--model: only with") {
		t.Fatalf("code %d %q", code, e.String())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// A write error on the answer is a usage-class failure, never a verdict.
func TestModelJSONWriteErrorIsNotAVerdict(t *testing.T) {
	now = func() time.Time { return t10 }
	f := teamFable()
	newBackend = func() (backend, error) { return f, nil }
	var e strings.Builder
	code := run([]string{"status", "--model", "sonnet", "-o", "json", "--stamp", filepath.Join(t.TempDir(), "s.json")}, failWriter{}, &e)
	if code != 2 || !strings.Contains(e.String(), "disk full") {
		t.Fatalf("code %d %q", code, e.String())
	}
}

func TestPlainTextSaysNoSeparateLimit(t *testing.T) {
	_, out, st := runStatus(t, teamFable(), "status")
	if !strings.Contains(out, "Weekly Sonnet      –   no separate limit on this plan   [N/A]") || strings.Contains(out, "not available") {
		t.Errorf("text:\n%s", out)
	}
	for _, w := range st.Windows {
		if w.Verdict == stamp.NotAvailable && !strings.Contains(w.Note, "no separate limit on this plan") {
			t.Errorf("note %q", w.Note)
		}
	}
}

func FuzzAnswerFor(f *testing.F) {
	f.Add("sonnet", "Weekly Sonnet", 100.0, "n/a")
	f.Add("", "", 0.0, "")
	f.Fuzz(func(t *testing.T, model, label string, used float64, verdict string) {
		st := stamp.Stamp{Windows: []stamp.Window{
			{Account: "a", Label: "Session (5hr)", Verdict: stamp.Available},
			{Account: "a", Label: label, UsedPercent: used, Verdict: stamp.Verdict(verdict)},
		}}
		a := answerFor(st, model)
		switch a.Verdict {
		case stamp.Available, stamp.Exhausted, stamp.Unknown:
		default:
			t.Fatalf("verdict %q", a.Verdict)
		}
		if a.Verdict == stamp.Exhausted && a.Blocking == "" {
			t.Fatal("exhausted without a named limit")
		}
	})
}
