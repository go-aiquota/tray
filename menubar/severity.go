// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package menubar draws the tray icon and menu: what the user sees at a
// glance (a colored dot for the worst account's status) and what they see
// on click (one row per account, its session/weekly percentages, and
// actions). It never touches the keyring, a plugin, or a window — it is
// purely "given the current known state, what does the tray show."
package menubar

import "github.com/go-aiquota/proto/quotapb"

// Severity is the tray icon's at-a-glance status — the worst thing true
// about ANY monitored account, since a menu-bar icon has to answer "is
// anything wrong" without being opened.
type Severity int

const (
	// SeverityUnknown is the state before any account has ever been
	// successfully polled (no accounts yet, or every poll has failed) —
	// distinct from SeverityOK, which means "polled fine, plenty of quota
	// left", so a user can tell "nothing to worry about" from "nothing
	// known yet".
	SeverityUnknown Severity = iota
	// SeverityOK means every account's every window is comfortably under
	// its warning threshold.
	SeverityOK
	// SeverityWarning means at least one account has a window at or above
	// warningThreshold but below criticalThreshold.
	SeverityWarning
	// SeverityCritical means at least one account has a window at or above
	// criticalThreshold — the state a user actually needs to notice.
	SeverityCritical
	// SeverityError means at least one account's most recent poll failed
	// (a plugin error, a network failure, an expired session) — distinct
	// from a known-high quota, since the RIGHT response differs (re-auth
	// the account vs. just wait for the window to reset).
	SeverityError
)

// severityRank orders severities worst-to-best-independent — used only to
// pick the worse of two when aggregating, not exposed (callers compare
// Severity values by identity/switch, not by rank, since "worse" is an
// aggregation detail, not a property of one severity in isolation).
var severityRank = map[Severity]int{
	SeverityError:    4,
	SeverityCritical: 3,
	SeverityWarning:  2,
	SeverityOK:       1,
	SeverityUnknown:  0,
}

// Thresholds are the fractions (0..1 of a window's Used/Limit) at which a
// window's severity steps up. Exported so a host can tune them (e.g. from a
// future settings UI) rather than them being a package constant nobody can
// reach.
type Thresholds struct {
	Warning  float64 // default 0.7
	Critical float64 // default 0.9
}

// DefaultThresholds matches what a reasonable user would want to be warned
// at without configuring anything: comfortable margin at 70%, real urgency
// at 90%.
var DefaultThresholds = Thresholds{Warning: 0.7, Critical: 0.9}

// AccountStatus is one account's latest known state, as the tray needs it:
// either a snapshot from its last successful poll, or the error from its
// last failed one (never both — a poll either succeeded or it didn't).
type AccountStatus struct {
	AccountID string
	Label     string
	Snapshot  *quotapb.QuotaSnapshot // nil if the last poll failed
	Err       error                  // nil if the last poll succeeded (or none has run yet)
}

// Severity returns s's own severity: SeverityError if its last poll
// failed, SeverityUnknown if none has run yet, else the worst of its
// snapshot's windows against t.
func (s AccountStatus) Severity(t Thresholds) Severity {
	if s.Err != nil {
		return SeverityError
	}
	if s.Snapshot == nil {
		return SeverityUnknown
	}
	worst := SeverityOK
	for _, w := range s.Snapshot.Windows {
		worst = worstOf(worst, windowSeverity(w, t))
	}
	return worst
}

// windowSeverity classifies one usage window against t. A window with no
// limit (Limit <= 0) reports SeverityUnknown rather than dividing by zero
// or claiming a false OK — a provider that hasn't told us the limit hasn't
// told us anything actionable.
func windowSeverity(w *quotapb.QuotaWindow, t Thresholds) Severity {
	if w.Limit <= 0 {
		return SeverityUnknown
	}
	frac := w.Used / w.Limit
	switch {
	case frac >= t.Critical:
		return SeverityCritical
	case frac >= t.Warning:
		return SeverityWarning
	default:
		return SeverityOK
	}
}

// Aggregate returns the worst severity across every account — what the
// tray icon itself shows. An empty list is SeverityUnknown (no accounts
// configured yet), matching a single account with no poll yet.
func Aggregate(accounts []AccountStatus, t Thresholds) Severity {
	worst := SeverityUnknown
	for _, a := range accounts {
		worst = worstOf(worst, a.Severity(t))
	}
	return worst
}

func worstOf(a, b Severity) Severity {
	if severityRank[b] > severityRank[a] {
		return b
	}
	return a
}
