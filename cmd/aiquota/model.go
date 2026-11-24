// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-aiquota/tray/stamp"
)

// noSeparateLimit is what a per-model window the plan does not list means: the
// model is governed by the session and weekly windows alone. It is NOT "blocked"
// and NOT "unmeasured" (a session once relayed a stale 429 as "Sonnet is
// blocked" because the text said N/A).
const noSeparateLimit = "no separate limit on this plan"

// modelLimit is one limit that applies to the asked model.
type modelLimit struct {
	Kind         string        `json:"kind"` // session, weekly_all or scoped
	Label        string        `json:"label"`
	UsedPercent  float64       `json:"used_percent"`
	ResetsAtUnix int64         `json:"resets_at_unix,omitempty"`
	Verdict      stamp.Verdict `json:"verdict"`
}

// modelAnswer is "can I use model X right now on the active plan?".
type modelAnswer struct {
	Model   string        `json:"model"`
	Verdict stamp.Verdict `json:"verdict"`
	// Scoped is true when the plan has a window of its own for the model.
	Scoped  bool         `json:"scoped"`
	Account string       `json:"account,omitempty"`
	Plan    string       `json:"plan,omitempty"`
	At      time.Time    `json:"at"`
	Limits  []modelLimit `json:"limits"`
	// Blocking names the exhausted limit; Reason is the one-line reading.
	Blocking string `json:"blocking,omitempty"`
	Reason   string `json:"reason"`
}

// modelStamp is the stamp plus the per-model answer, for -o json.
type modelStamp struct {
	stamp.Stamp
	Model modelAnswer `json:"model"`
}

func isSession(label string) bool { return strings.HasPrefix(strings.ToLower(label), "session") }

func isWeeklyAll(label string) bool {
	l := strings.ToLower(label)
	return l == "weekly (7 day)" || l == "weekly_all"
}

// answerFor computes the verdict for model on the active login (the first
// account in the stamp) from its windows: the session, the weekly window and,
// when the plan lists one, the window scoped to the model. A scoped window
// listed without a value, or absent, is "no separate limit", never UNKNOWN.
func answerFor(st stamp.Stamp, model string) modelAnswer {
	a := modelAnswer{Model: model, Verdict: stamp.Unknown, At: st.At}
	want := strings.ToLower(strings.TrimSpace(model))
	acct := ""
	if len(st.Windows) > 0 {
		acct = st.Windows[0].Account
	}
	a.Account = acct
	sawRead := false
	for _, w := range st.Windows {
		if w.Account != acct {
			continue
		}
		if a.Plan == "" {
			a.Plan = w.Plan
		}
		var kind string
		switch {
		case isSession(w.Label):
			kind = "session"
		case isWeeklyAll(w.Label):
			kind = "weekly_all"
		case want != "" && strings.Contains(strings.ToLower(w.Label), want):
			if w.Verdict == stamp.NotAvailable {
				sawRead = true // listed without a value: no separate limit
				continue
			}
			kind = "scoped"
			a.Scoped = true
		default:
			continue
		}
		if w.Verdict == stamp.Available || w.Verdict == stamp.Exhausted {
			sawRead = true
		}
		a.Limits = append(a.Limits, modelLimit{Kind: kind, Label: w.Label, UsedPercent: w.UsedPercent, ResetsAtUnix: w.ResetsAtUnix, Verdict: w.Verdict})
	}
	exhausted, unknown := -1, false
	for i, l := range a.Limits {
		switch l.Verdict {
		case stamp.Exhausted:
			if exhausted < 0 {
				exhausted = i
			}
		case stamp.Available:
		default:
			unknown = true
		}
	}
	switch {
	case exhausted >= 0:
		l := a.Limits[exhausted]
		a.Verdict, a.Blocking = stamp.Exhausted, l.Label
		a.Reason = fmt.Sprintf("%s exhausted (%d%%), resets %s", l.Label, panelPercent(l.UsedPercent), resetOf(l, st.At))
	case !sawRead || unknown || len(a.Limits) == 0:
		a.Verdict = stamp.Unknown
		a.Reason = "could not read the limits"
		if len(st.Problems) > 0 {
			a.Reason += ": " + strings.Join(st.Problems, "; ")
		}
	default:
		a.Verdict = stamp.Available
		if a.Scoped {
			a.Reason = "within every limit that applies to " + model
		} else {
			a.Reason = "no separate " + model + " limit on this plan; governed by the session and weekly limits"
		}
	}
	return a
}

func resetOf(l modelLimit, at time.Time) string {
	if l.ResetsAtUnix <= 0 {
		return "at an unknown time"
	}
	return panelReset(l.ResetsAtUnix, l.Label, at)
}

func renderModel(w io.Writer, st stamp.Stamp, a modelAnswer) {
	fmt.Fprintf(w, "measured %s\n", st.At.Local().Format("2006-01-02 15:04 MST"))
	if st.Stale != "" {
		fmt.Fprintf(w, "stale: %s\n", st.Stale)
	}
	if a.Account != "" {
		plan := a.Plan
		if plan == "" {
			plan = "unknown"
		}
		fmt.Fprintf(w, "Account: %s\nPlan: %s\n", a.Account, plan)
	}
	if !a.Scoped && a.Verdict != stamp.Unknown {
		fmt.Fprintf(w, "no separate %s limit on this plan\n", a.Model)
	}
	for _, l := range a.Limits {
		line := fmt.Sprintf("%-16s %3d%%", l.Label, panelPercent(l.UsedPercent))
		if l.ResetsAtUnix > 0 {
			line += "   Resets " + panelReset(l.ResetsAtUnix, l.Label, now())
		}
		fmt.Fprintf(w, "%s   [%s]\n", line, strings.ToUpper(string(l.Verdict)))
	}
	fmt.Fprintf(w, "%s: %s: %s\n", a.Model, strings.ToUpper(string(a.Verdict)), a.Reason)
}
