// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-aiquota/proto/quotapb"
	"github.com/go-widgets/tray"
)

// Actions are the callbacks BuildMenu wires to the menu's non-account rows.
// A nil field is treated as a no-op: a caller that hasn't wired one yet gets
// a clickable-but-inert row rather than a crash.
type Actions struct {
	// AddAccount opens the onboarding window for a new account of the given
	// provider (see ProviderChoice.Provider) — always called with exactly
	// one of the providers BuildMenu was given, never "" or an unlisted one.
	AddAccount func(provider string)
	// LockNow discards every cached credential (app.CredentialCache.Lock),
	// so the next poll of a Touch-ID-gated account prompts again instead of
	// reusing the copy the tray has held since it last asked. It has
	// nothing to do with the OS keychain itself — an account's credential
	// was never unprotected on disk — only with how long THIS PROCESS
	// trusts a copy it already asked permission for.
	LockNow func()
	// Quit ends the application.
	Quit func()
}

// ProviderChoice is one entry in the "Add account…" picker: Provider is
// what Actions.AddAccount is called with (a provider plugin's own
// Describe().Name — "claude", "chatgpt", …), Label is what a person sees.
// The list BuildMenu is given comes from discovering installed plugins
// (see quota.DiscoverProviders) — a new plugin shows up here without any
// change to this package, since nothing here hardcodes a provider name.
type ProviderChoice struct {
	Provider string
	Label    string
}

// BuildMenu renders one disabled (label-only) row per account — a menu bar
// is for glancing at, not editing; adding or removing an account is
// deliberately routed through AddAccount/onboarding rather than a per-row
// action menu here, since that's account-management UI, not a status
// readout — followed by a separator and the actions every tray needs
// regardless of how many accounts exist. providers drives the "Add
// account…" row: a plain item when there's exactly one, a submenu (one row
// per provider) when there's more than one, disabled when there's none.
func BuildMenu(accounts []AccountStatus, t Thresholds, actions Actions, providers []ProviderChoice) *tray.Menu {
	m := tray.NewMenu()
	if len(accounts) == 0 {
		m.Add(&tray.MenuItem{Label: "No accounts yet", Disabled: true})
	}
	now := time.Now()
	for _, a := range accounts {
		m.Add(&tray.MenuItem{Label: accountRow(a, t, now), Disabled: true})
	}
	m.Add(tray.Separator())
	m.Add(addAccountItem(providers, actions.AddAccount))
	m.Add(tray.Item("Lock now", orNoop(actions.LockNow)))
	m.Add(tray.Item("Quit", orNoop(actions.Quit)))
	return m
}

// addAccountItem builds the "Add account…" row itself. addAccount may be
// nil (see Actions' own doc comment) — every generated click handler goes
// through it defensively rather than assuming a caller always wires one.
func addAccountItem(providers []ProviderChoice, addAccount func(string)) *tray.MenuItem {
	click := func(provider string) func() {
		return func() {
			if addAccount != nil {
				addAccount(provider)
			}
		}
	}
	switch len(providers) {
	case 0:
		return &tray.MenuItem{Label: "Add account… (no provider plugins found)", Disabled: true}
	case 1:
		return tray.Item("Add account…", click(providers[0].Provider))
	default:
		sub := tray.NewMenu()
		for _, p := range providers {
			label := p.Label
			if label == "" {
				label = p.Provider
			}
			sub.Add(tray.Item(label, click(p.Provider)))
		}
		return tray.SubMenu("Add account…", sub)
	}
}

// accountRow renders one account's line, e.g. "work@example.com — session
// 45% (resets in 2h14m), weekly 78%", or its error in place of percentages
// when its last poll failed — the same distinction Severity itself draws,
// so the menu never shows a stale percentage next to an account that's
// actually broken. now is read once by the caller (not time.Now() per
// window) so every row in the same menu build agrees on "now".
func accountRow(a AccountStatus, t Thresholds, now time.Time) string {
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
		parts = append(parts, windowRow(w, now))
	}
	return name + " — " + strings.Join(parts, ", ")
}

// windowRow renders one usage window as "label NN% (resets in DURATION)",
// or "label: n/a" when the provider hasn't reported a limit (see
// windowSeverity, which treats the same case as Unknown rather than a
// false 0%). The reset clause is omitted when the provider didn't report
// ResetsAtUnix (0 — plugin-claude always does; a future provider that
// can't know its own reset time simply doesn't set it) or it has already
// passed (stale between this poll and the provider's own reset, about to
// self-correct on the next one — showing a negative duration would be
// worse than showing nothing).
func windowRow(w *quotapb.QuotaWindow, now time.Time) string {
	if w.Limit <= 0 {
		return fmt.Sprintf("%s: n/a", w.Label)
	}
	row := fmt.Sprintf("%s %.0f%%", w.Label, w.Used/w.Limit*100)
	if w.ResetsAtUnix > 0 {
		if d := time.Unix(w.ResetsAtUnix, 0).Sub(now); d > 0 {
			row += fmt.Sprintf(" (resets in %s)", formatResetIn(d))
		}
	}
	return row
}

// formatResetIn renders a duration the way a person reads a countdown, not
// the way time.Duration.String does (no seconds — nobody needs "2h14m9s"
// worth of precision for a quota window, and it would tick over on every
// menu build besides): "42m", "2h14m", or "1d3h" past a day.
func formatResetIn(d time.Duration) string {
	d = d.Round(time.Minute)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

func orNoop(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	return fn
}
