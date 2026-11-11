// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aiquota/proto/quotapb"
	"github.com/go-aiquota/tray/stamp"
)

func TestBandEdges(t *testing.T) {
	for _, c := range []struct {
		pct  float64
		n    int
		band int
		name string
	}{
		{0, 8, 0, "light green"},
		{12.4, 8, 0, "light green"},
		{12.5, 8, 1, "dark green"},
		{24.99, 8, 1, "dark green"},
		{25, 8, 2, "light yellow"},
		{37.5, 8, 3, "dark yellow"},
		{50, 8, 4, "light orange"},
		{62.5, 8, 5, "dark orange"},
		{75, 8, 6, "light red"},
		{87.5, 8, 7, "dark red"},
		{99.9, 8, 7, "dark red"},
		{100, 8, 7, "dark red"},
		{250, 8, 7, "dark red"},
		{-3, 8, 0, "light green"},
		{49.9, 2, 0, "light green"},
		{50, 2, 1, "dark red"},
		{40, 3, 1, "light orange"}, // 3 bands: first, middle, last step
		{66.7, 3, 2, "dark red"},
	} {
		band, s := bandOf(c.pct, c.n)
		if band != c.band || s.Name != c.name {
			t.Errorf("bandOf(%v, %d) = %d %q, want %d %q", c.pct, c.n, band, s.Name, c.band, c.name)
		}
	}
}

func TestOnlyWarningAndErrorBackgrounds(t *testing.T) {
	for i, s := range palette {
		want := bgNone
		switch i {
		case 6:
			want = bgWarning
		case 7:
			want = bgError
		}
		if s.Background != want {
			t.Errorf("step %d %s: background %s, want %s", i, s.Name, s.Background, want)
		}
	}
}

func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil || len(hex) != 7 {
		t.Fatalf("bad colour %q", hex)
	}
	lin := func(c float64) float64 {
		c /= 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(float64(v>>16&255)) + 0.7152*lin(float64(v>>8&255)) + 0.0722*lin(float64(v&255))
}

func contrast(t *testing.T, a, b string) float64 {
	la, lb := luminance(t, a), luminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// The status bar's default background is #007ACC in both Dark+ and Light+
// (src/vs/workbench/common/theme.ts, statusBar.background) and #68217A with no
// folder open (statusBar.noFolderBackground). Every foreground painted on it
// needs WCAG contrast >= 3:1 against both. Steps with a theme background are
// drawn by the theme (white text) and are exempt.
func TestForegroundContrastOnTheDefaultStatusBar(t *testing.T) {
	cols := map[string]string{}
	for _, s := range palette {
		if s.Background == bgNone {
			cols[s.Name] = s.Color
		}
	}
	cols["unknown"] = unknownColor
	for name, c := range cols {
		for _, bg := range []string{"#007ACC", "#68217A"} {
			r := contrast(t, c, bg)
			t.Logf("%-13s %s on %s: %.2f", name, c, bg, r)
			if r < 3 {
				t.Errorf("%s %s on %s: contrast %.2f < 3", name, c, bg, r)
			}
		}
	}
}

func TestItemsPerWindow(t *testing.T) {
	st := stamp.Stamp{Version: stamp.Version, At: t10, Verdict: stamp.Available, Windows: []stamp.Window{
		{Account: "Claude Code", Label: "Session (5hr)", Limit: 100, UsedPercent: 12.5, Verdict: stamp.Available, Plan: "team"},
		{Account: "Claude Code", Label: "Weekly (7 day)", Limit: 100, UsedPercent: 80, Verdict: stamp.Available, Plan: "team"},
		{Account: "Claude Code", Label: "Weekly Fable", Limit: 100, UsedPercent: 0, Verdict: stamp.Available, Plan: "team"},
		{Account: "Claude Code", Label: "Weekly Sonnet", Limit: 100, UsedPercent: 99.9, Verdict: stamp.Available, Plan: "team"},
	}}
	sb := buildStatusBar(st, 75, 90, 8)
	// A window at 0 % is drawn too: hiding it made the Fable window vanish
	// from the bar whenever it was unused.
	if sb.Version != 2 || len(sb.Items) != 4 {
		t.Fatalf("%+v", sb)
	}
	want := []barItem{
		{ID: "w0", Text: "Claude 5h 12%", Percent: 12, Band: 1, Color: "#5CF29A", Background: bgNone},
		{ID: "w1", Text: "7d 80%", Percent: 80, Band: 6, Color: "#FFC9C2", Background: bgWarning},
		{ID: "w2", Text: "Fable 0%", Percent: 0, Band: 0, Color: "#B9F6CA", Background: bgNone},
		{ID: "w3", Text: "Sonnet 99%", Percent: 99, Band: 7, Color: "#FFD2CC", Background: bgError},
	}
	for i, w := range want {
		if sb.Items[i] != w {
			t.Errorf("item %d: %+v, want %+v", i, sb.Items[i], w)
		}
	}
}

// A full window carries its reset time in the item itself; one below 100 %
// does not, and neither does a full window with no reset time.
func TestFullWindowShowsItsResetTime(t *testing.T) {
	old := now
	t.Cleanup(func() { now = old })
	at := time.Date(2026, 11, 11, 9, 30, 0, 0, time.Local)
	now = func() time.Time { return at }
	st := stamp.Stamp{Version: stamp.Version, At: at, Verdict: stamp.Exhausted, Windows: []stamp.Window{
		{Account: "Claude Code", Label: "Session (5hr)", UsedPercent: 100, Verdict: stamp.Exhausted, ResetsAtUnix: at.Add(4*time.Hour + 50*time.Minute).Unix()},
		{Account: "Claude Code", Label: "Weekly (7 day)", UsedPercent: 100, Verdict: stamp.Exhausted, ResetsAtUnix: time.Date(2026, 11, 13, 3, 0, 0, 0, time.Local).Unix()},
		{Account: "Claude Code", Label: "Weekly Fable", UsedPercent: 99.9, Verdict: stamp.Available, ResetsAtUnix: at.Add(time.Hour).Unix()},
		{Account: "Claude Code", Label: "Weekly Sonnet", UsedPercent: 100, Verdict: stamp.Exhausted},
	}}
	sb := buildStatusBar(st, 75, 90, 8)
	for i, want := range []string{"Claude 5h 100% → 2026-11-11 14:20", "7d 100% → 2026-11-13 03:00", "Fable 99%", "Sonnet 100%"} {
		if sb.Items[i].Text != want {
			t.Errorf("item %d: %q, want %q", i, sb.Items[i].Text, want)
		}
	}
	for _, c := range []struct {
		reset time.Time
		want  string
	}{
		{at.Add(-time.Minute), "soon"},
		{time.Date(2026, 11, 11, 23, 59, 0, 0, time.Local), "2026-11-11 23:59"},
		{time.Date(2027, 1, 2, 0, 1, 0, 0, time.Local), "2027-01-02 00:01"},
	} {
		if got := barReset(c.reset.Unix(), at); got != c.want {
			t.Errorf("barReset(%s) = %q, want %q", c.reset, got, c.want)
		}
	}
}

// A window the provider lists without a value is drawn as a grey "–", says
// why in the tooltip, and takes no part in the verdict.
func TestNotAvailableWindowIsDashedAndNeverANumber(t *testing.T) {
	f := one(nil)
	f.ccErr = nil
	f.cc = &quotapb.QuotaSnapshot{PlanKind: "team", Windows: []*quotapb.QuotaWindow{
		win("Session (5hr)", 30, 0), win("Weekly (7 day)", 5, 0),
		{Label: "Weekly Sonnet", Unit: unitNotAvailable},
		{Label: "Weekly Opus", Unit: unitNotAvailable},
	}}
	code, out, st := runStatus(t, f, "status", "-o", "statusbar", "--format-version", "2")
	if code != 0 || st.Verdict != stamp.Available {
		t.Fatalf("n/a windows must not change the verdict: code %d %s", code, st.Verdict)
	}
	var sb statusBar
	if err := json.Unmarshal([]byte(out), &sb); err != nil || len(sb.Items) != 4 {
		t.Fatalf("%v %s", err, out)
	}
	for _, it := range sb.Items[2:] {
		if it.Percent != 0 || it.Band != -1 || it.Color != unknownColor || it.Background != bgNone || !strings.HasSuffix(it.Text, " –") {
			t.Errorf("n/a item: %+v", it)
		}
	}
	if !strings.Contains(sb.Tooltip, "Weekly Sonnet  –  not available") || sb.Text != "Claude 30% · 5%" {
		t.Errorf("%q / %q", sb.Text, sb.Tooltip)
	}
	_, text, _ := runStatus(t, f, "status")
	if !strings.Contains(text, "Weekly Opus") || !strings.Contains(text, "not available   [N/A]") {
		t.Errorf("text output:\n%s", text)
	}
}

func TestOnlyNotAvailableWindowsMeanNothingWasMeasured(t *testing.T) {
	if v := stamp.Overall([]stamp.Window{{Verdict: stamp.NotAvailable}, {Verdict: stamp.NotAvailable}}); v != stamp.Unknown {
		t.Errorf("verdict %s, want unknown", v)
	}
	if v := stamp.Overall([]stamp.Window{{Verdict: stamp.NotAvailable}, {Verdict: stamp.Exhausted}}); v != stamp.Exhausted {
		t.Errorf("verdict %s, want exhausted", v)
	}
}

func TestOverlayFlagAsksForThePanelView(t *testing.T) {
	claudeCodeView = ""
	t.Cleanup(func() { claudeCodeView = "" })
	runStatus(t, one(nil), "status", "--overlay", "--account", "max")
	if claudeCodeView != "overlay" {
		t.Errorf("view %q", claudeCodeView)
	}
}

func TestRawPrintsTheProviderResponseAndOnlyWithJSON(t *testing.T) {
	old := rawUsage
	t.Cleanup(func() { rawUsage = old })
	rawUsage = func(context.Context, string, time.Duration) ([]byte, error) {
		return []byte("{\"five_hour\": null}\n"), nil
	}
	var o, e bytes.Buffer
	if code := run([]string{"status", "-o", "json", "--raw"}, &o, &e); code != 0 || o.String() != "{\"five_hour\": null}\n" {
		t.Errorf("code %d out %q err %q", code, o.String(), e.String())
	}
	if code := run([]string{"status", "--raw"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Errorf("--raw without -o json: code %d, want 2", code)
	}
	rawUsage = func(context.Context, string, time.Duration) ([]byte, error) {
		return nil, errors.New("boom sk-ant-sid01-abcdefghijklmnopqrstuvwxyz0123456789")
	}
	o.Reset()
	e.Reset()
	if code := run([]string{"status", "-o", "json", "--raw"}, &o, &e); code != 3 || strings.Contains(e.String(), "abcdefghijklmnop") || o.Len() != 0 {
		t.Errorf("failure: code %d out %q err %q", code, o.String(), e.String())
	}
}

func TestUnknownItemIsGreyAndHasNoNumber(t *testing.T) {
	sb := buildStatusBar(stamp.Stamp{At: t10, Verdict: stamp.Unknown, Problems: []string{"boom"}}, 75, 90, 8)
	if len(sb.Items) != 1 || sb.Items[0].Text != "Claude ?" || sb.Items[0].Band != -1 || sb.Items[0].Color != unknownColor || sb.Items[0].Background != bgNone {
		t.Errorf("%+v", sb.Items)
	}
}

func runArgs(t *testing.T, args ...string) int {
	t.Helper()
	newBackend = func() (backend, error) { return one(nil), nil }
	return run(args, &bytes.Buffer{}, &bytes.Buffer{})
}

// A client asks for the contract version it understands; the default is 1,
// byte-for-byte the shape v0.3.0 printed, so the first extension release keeps
// working against a newer binary.
func TestFormatVersionOneIsTheOldShapeAndTwoAddsItems(t *testing.T) {
	f := one(nil)
	f.ccErr = nil
	f.cc = &quotapb.QuotaSnapshot{PlanKind: "team", Windows: []*quotapb.QuotaWindow{win("Session (5hr)", 30, 0), win("Weekly (7 day)", 5, 0)}}
	_, out, _ := runStatus(t, f, "status", "-o", "statusbar")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 5 || raw["items"] != nil || string(raw["version"]) != "1" {
		t.Errorf("default is not the v1 shape: %s", out)
	}
	_, out, _ = runStatus(t, f, "status", "-o", "statusbar", "--format-version", "2")
	var sb statusBar
	if err := json.Unmarshal([]byte(out), &sb); err != nil || sb.Version != 2 || len(sb.Items) != 2 {
		t.Errorf("v2: %v %s", err, out)
	}
	if code := runArgs(t, "status", "-o", "statusbar", "--format-version", "3"); code != 2 {
		t.Errorf("--format-version 3: code %d, want 2", code)
	}
}

func TestBadBandsAreUsageError(t *testing.T) {
	for _, n := range []string{"1", "9", "0"} {
		if code := runArgs(t, "status", "-o", "statusbar", "--bands", n); code != 2 {
			t.Errorf("--bands %s: code %d, want 2", n, code)
		}
	}
}
