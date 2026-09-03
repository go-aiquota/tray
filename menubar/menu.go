// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"fmt"
	"strings"

	"github.com/go-aiquota/proto/quotapb"
	"github.com/go-widgets/tray"
)

// Actions are the callbacks BuildMenu wires to the menu's non-account rows.
// A nil field is treated as a no-op: a caller that hasn't wired one yet gets
// a clickable-but-inert row rather than a crash.
type Actions struct {
	// AddAccount opens the onboarding window for a new account.
	AddAccount func()
	// Quit ends the application.
	Quit func()
}

// BuildMenu renders one disabled (label-only) row per account — a menu bar
// is for glancing at, not editing; adding or removing an account is
// deliberately routed through AddAccount/onboarding rather than a per-row
// action menu here, since that's account-management UI, not a status
// readout — followed by a separator and the actions every tray needs
// regardless of how many accounts exist.
func BuildMenu(accounts []AccountStatus, t Thresholds, actions Actions) *tray.Menu {
	m := tray.NewMenu()
	if len(accounts) == 0 {
		m.Add(&tray.MenuItem{Label: "No accounts yet", Disabled: true})
	}
	for _, a := range accounts {
		m.Add(&tray.MenuItem{Label: accountRow(a, t), Disabled: true})
	}
	m.Add(tray.Separator())
	m.Add(tray.Item("Add account…", orNoop(actions.AddAccount)))
	m.Add(tray.Item("Quit", orNoop(actions.Quit)))
	return m
}

// accountRow renders one account's line, e.g. "work@example.com — session
// 45%, weekly 78%", or its error in place of percentages when its last poll
// failed — the same distinction Severity itself draws, so the menu never
// shows a stale percentage next to an account that's actually broken.
func accountRow(a AccountStatus, t Thresholds) string {
	name := a.Label
	if name == "" {
		name = a.AccountID
	}
	switch {
	case a.Err != nil:
		return fmt.Sprintf("%s — %v", name, a.Err)
	case a.Snapshot == nil:
		return fmt.Sprintf("%s — not polled yet", name)
	case len(a.Snapshot.Windows) == 0:
		return fmt.Sprintf("%s — no usage data", name)
	}
	parts := make([]string, 0, len(a.Snapshot.Windows))
	for _, w := range a.Snapshot.Windows {
		parts = append(parts, windowRow(w))
	}
	return name + " — " + strings.Join(parts, ", ")
}

// windowRow renders one usage window as "label NN%", or "label: n/a" when
// the provider hasn't reported a limit (see windowSeverity, which treats
// the same case as Unknown rather than a false 0%).
func windowRow(w *quotapb.QuotaWindow) string {
	if w.Limit <= 0 {
		return fmt.Sprintf("%s: n/a", w.Label)
	}
	return fmt.Sprintf("%s %.0f%%", w.Label, w.Used/w.Limit*100)
}

func orNoop(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	return fn
}
