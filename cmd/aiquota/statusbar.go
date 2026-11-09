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

// statusBar is the whole contract with the VS Code extension (-o statusbar):
// the text and tooltip are shown as they are.
type statusBar struct {
	Version int    `json:"version"`
	Text    string `json:"text"`
	Tooltip string `json:"tooltip"`
	Level   string `json:"level"`
	Verdict string `json:"verdict"`
}

// buildStatusBar derives the status bar item from a stamp. An UNKNOWN verdict
// is "Claude ?" with the reasons in the tooltip, never a number. The level is
// the worst window against warn and critical (percent); an exhausted window
// is always an error.
func buildStatusBar(st stamp.Stamp, warn, critical int) statusBar {
	sb := statusBar{Version: 1, Verdict: string(st.Verdict)}
	at := "measured " + st.At.Local().Format("2006-01-02 15:04:05 MST")
	if st.Verdict == stamp.Unknown {
		sb.Text, sb.Level = "Claude ?", levelUnknown
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
		if i < 2 { // session and weekly, as the panel lists them first
			parts = append(parts, fmt.Sprintf("%d%%", p))
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

func renderStatusBar(w io.Writer, st stamp.Stamp, warn, critical int) error {
	return json.NewEncoder(w).Encode(buildStatusBar(st, warn, critical))
}
