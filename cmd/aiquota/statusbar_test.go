// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"testing"

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
