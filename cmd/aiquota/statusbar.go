// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/go-aiquota/tray/stamp"
)

// Levels a status bar can colour by; the editor extension maps them to
// theme colours and holds no threshold of its own.
const (
	levelOK      = "ok"
	levelWarn    = "warn"
	levelError   = "error"
	levelUnknown = "unknown"
)

// Backgrounds an item may ask for. VS Code's extension API honours only the
// theme's warning and error backgrounds (StatusBarItem.backgroundColor,
// extHostStatusBar.ts ALLOWED_BACKGROUND_COLORS), so those are the only two.
const (
	bgNone    = "none"
	bgWarning = "warning"
	bgError   = "error"
)

// step is one band of the 8-step scale. Color is the foreground hex, chosen
// for WCAG contrast >= 3:1 against the default status bar background
// (#007ACC in Dark+ and Light+, #68217A with no folder open); a step that
// carries a background is painted by the theme and its colour is only a hint.
type step struct{ Name, Color, Background string }

// palette runs light green, dark green, light yellow, dark yellow, light
// orange, dark orange, then the theme's warning and error backgrounds. Text
// on a mid-blue bar cannot be darker than L=0.6, so a "dark" step is the
// more saturated one, not the less bright one.
var palette = [8]step{
	{"light green", "#B9F6CA", bgNone},
	{"dark green", "#5CF29A", bgNone},
	{"light yellow", "#FFF59D", bgNone},
	{"dark yellow", "#FFD740", bgNone},
	{"light orange", "#FFE0B2", bgNone},
	{"dark orange", "#FFCC80", bgNone},
	{"light red", "#FFC9C2", bgWarning},
	{"dark red", "#FFD2CC", bgError},
}

// unknownColor is the neutral grey of an unreadable usage.
const unknownColor = "#DADADA"

// bandOf is the band 0..n-1 of a percentage in n equal steps (n = 8: 12.5 %
// each; 100 and more are the last band), and the palette step it paints with.
func bandOf(percent float64, n int) (band int, s step) {
	band = int(percent * float64(n) / 100)
	if band < 0 {
		band = 0
	}
	if band > n-1 {
		band = n - 1
	}
	idx := band
	if n < len(palette) {
		idx = (band*(len(palette)-1) + (n-1)/2) / (n - 1)
	}
	return band, palette[idx]
}

// barItem is one status bar item: a window, painted by its own band.
type barItem struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	Percent    int    `json:"percent"`
	Band       int    `json:"band"` // -1 when unknown
	Color      string `json:"color"`
	Background string `json:"background"`
}

// statusBar is the whole contract with the VS Code extension (-o statusbar):
// the items and the tooltip are shown as they are.
type statusBar struct {
	Version int       `json:"version"`
	Text    string    `json:"text"`
	Tooltip string    `json:"tooltip"`
	Level   string    `json:"level"`
	Verdict string    `json:"verdict"`
	Items   []barItem `json:"items,omitempty"`
}

// shortLabel is the compact name of a window for an item.
func shortLabel(label string) string {
	l := strings.ToLower(label)
	switch {
	case strings.HasPrefix(l, "session"):
		return "5h"
	case strings.HasPrefix(l, "weekly (7"):
		return "7d"
	}
	return strings.TrimSpace(strings.TrimPrefix(label, "Weekly"))
}

// buildStatusBar derives the status bar from a stamp. An UNKNOWN verdict is
// one grey "Claude ?" item with the reasons in the tooltip, never a number.
// The level is the worst window against warn and critical (percent); an
// exhausted window is always an error. Items: the first two windows (session,
// weekly) always, any further window (per-model) only when above zero.
func buildStatusBar(st stamp.Stamp, warn, critical, bands int) statusBar {
	sb := statusBar{Version: 2, Verdict: string(st.Verdict)}
	at := "measured " + st.At.Local().Format("2006-01-02 15:04:05 MST")
	if st.Verdict == stamp.Unknown {
		sb.Text, sb.Level = "Claude ?", levelUnknown
		sb.Items = []barItem{{ID: "unknown", Text: "Claude ?", Band: -1, Color: unknownColor, Background: bgNone}}
		reasons := append([]string{}, st.Problems...)
		for _, w := range st.Windows {
			if w.Note != "" {
				reasons = append(reasons, w.Label+": "+w.Note)
			}
		}
		if len(reasons) == 0 {
			reasons = []string{"usage could not be read"}
		}
		sb.Tooltip = "Claude usage unknown\n" + strings.Join(reasons, "\n") + "\n" + at
		return sb
	}
	var parts []string
	worst := 0
	for i, w := range st.Windows {
		p := panelPercent(w.UsedPercent)
		if p > worst {
			worst = p
		}
		if i < 2 {
			parts = append(parts, fmt.Sprintf("%d%%", p))
		}
		if i < 2 || w.UsedPercent > 0 {
			band, s := bandOf(w.UsedPercent, bands)
			text := fmt.Sprintf("%s %d%%", shortLabel(w.Label), p)
			if i == 0 {
				text = "Claude " + text
			}
			sb.Items = append(sb.Items, barItem{ID: fmt.Sprintf("w%d", i), Text: text, Percent: p, Band: band, Color: s.Color, Background: s.Background})
		}
	}
	sb.Text = "Claude " + strings.Join(parts, " · ")
	switch {
	case st.Verdict == stamp.Exhausted || worst >= critical:
		sb.Level = levelError
	case worst >= warn:
		sb.Level = levelWarn
	default:
		sb.Level = levelOK
	}
	var b strings.Builder
	last := ""
	for _, w := range st.Windows {
		if w.Account != last {
			last = w.Account
			plan := w.Plan
			if plan == "" {
				plan = "unknown"
			}
			fmt.Fprintf(&b, "%s (%s)\n", w.Account, plan)
		}
		fmt.Fprintf(&b, "%s  %d%%", w.Label, panelPercent(w.UsedPercent))
		if w.ResetsAtUnix > 0 {
			b.WriteString("  resets " + panelReset(w.ResetsAtUnix, w.Label, now()))
		}
		b.WriteString("\n")
	}
	b.WriteString(at)
	sb.Tooltip = b.String()
	return sb
}

// renderStatusBar prints the contract at the version the client asked for.
// Version 1 is exactly what v0.3.0 printed (no items), so an older client
// keeps working against a newer binary; version 2 adds the items.
func renderStatusBar(w io.Writer, st stamp.Stamp, warn, critical, bands, version, render int) error {
	sb := buildStatusBar(st, warn, critical, bands)
	if render == renderV1 {
		return json.NewEncoder(w).Encode(buildRender(sb))
	}
	if version == 1 {
		sb.Version, sb.Items = 1, nil
	}
	return json.NewEncoder(w).Encode(sb)
}
