// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"fmt"
	"strings"
	"sync"

	"github.com/go-widgets/tray"
)

// AccountItems manages one tray.Tray PER ACCOUNT, each Attached to the
// same platform loop the control Tray (see Open) Holds — a Stats-style
// display: an account's own item shows its usage as TEXT directly in the
// menu bar ("6%/13%"), rather than making a person open a menu to see
// anything at all.
//
// Safe for concurrent Sync/Close/Len calls: a host's own poll loop calls
// Sync from more than one goroutine in practice (a ticker's own goroutine,
// and an "account added" handler running on whatever goroutine processes
// that action), and nothing here assumes otherwise.
type AccountItems struct {
	thresholds Thresholds
	actions    Actions

	mu    sync.Mutex
	items map[string]*tray.Tray
}

// NewAccountItems returns a manager with no items yet; call Sync to
// populate it. actions and thresholds are shared across every item's own
// menu (see Sync) exactly as they are for the control item's.
func NewAccountItems(actions Actions, t Thresholds) *AccountItems {
	return &AccountItems{actions: actions, thresholds: t, items: map[string]*tray.Tray{}}
}

// Sync makes the live items match accounts: creates one for each account
// not already showing, updates every existing one's icon/title/tooltip/
// menu, and Closes the item for any account no longer present — removing
// THAT ONE item (see Tray.Close) without touching the shared platform loop
// every other item, and the control item Holding it, depends on.
//
// It must be called only once that loop actually exists — from
// menubar.Tray's OnReady callback the first time, and freely thereafter
// (a ticker tick, an account added or removed) since the loop is then
// already running.
func (a *AccountItems) Sync(accounts []AccountStatus) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	seen := make(map[string]bool, len(accounts))
	for _, status := range accounts {
		seen[status.AccountID] = true
		if err := a.upsertLocked(status); err != nil {
			return err
		}
	}
	for id, t := range a.items {
		if !seen[id] {
			t.Close()
			delete(a.items, id)
		}
	}
	return nil
}

// upsertLocked does the actual work; callers must hold a.mu.
func (a *AccountItems) upsertLocked(status AccountStatus) error {
	icon, err := Icon(IconPx, status.Severity(a.thresholds))
	if err != nil {
		return fmt.Errorf("menubar: building %s's icon: %w", status.AccountID, err)
	}
	title := titleFor(status)
	tooltip := accountRow(status, a.thresholds)
	// The SAME BuildMenu the control item uses, given just this one
	// account: one disabled detail row plus the shared actions, so every
	// item — control or per-account — offers identical control, and there
	// is no second menu-building path to keep in sync with the first.
	menu := BuildMenu([]AccountStatus{status}, a.thresholds, a.actions)

	if t, ok := a.items[status.AccountID]; ok {
		t.SetIcon(icon).SetTitle(title).SetTooltip(tooltip).SetMenu(menu)
		return nil
	}
	t := tray.New(icon)
	if b := trayBackend(); b != nil {
		t = t.WithBackend(b)
	}
	t.SetTitle(title).SetTooltip(tooltip).SetMenu(menu)
	if err := t.Attach(); err != nil {
		return fmt.Errorf("menubar: attaching %s's item: %w", status.AccountID, err)
	}
	a.items[status.AccountID] = t
	return nil
}

// Close removes every item this manager has created (see Tray.Close),
// leaving no items behind.
func (a *AccountItems) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, t := range a.items {
		t.Close()
		delete(a.items, id)
	}
}

// Len reports how many items are currently live — for tests, and for a
// caller that wants to know whether ANY per-account item exists yet.
func (a *AccountItems) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.items)
}

// titleFor is the compact text drawn in the menu bar for status: the
// session and weekly-all percentages — the two windows a person watching
// their quota cares about moment to moment — not every window BuildMenu's
// own detail row lists (a scoped, per-model window would not fit in real
// menu-bar space next to a dozen other icons).
func titleFor(status AccountStatus) string {
	if status.Err != nil {
		return "⚠"
	}
	if status.Snapshot == nil || len(status.Snapshot.Windows) == 0 {
		return "…"
	}
	var parts []string
	for _, w := range status.Snapshot.Windows {
		label := strings.ToLower(w.Label)
		isWeeklyAll := strings.Contains(label, "weekly") && !strings.Contains(label, "scoped")
		if !strings.Contains(label, "session") && !isWeeklyAll {
			continue
		}
		if w.Limit <= 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%.0f%%", w.Used/w.Limit*100))
	}
	if len(parts) == 0 {
		return "…"
	}
	return strings.Join(parts, "/")
}
