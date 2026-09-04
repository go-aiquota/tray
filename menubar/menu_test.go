// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-aiquota/proto/quotapb"
	"github.com/go-widgets/tray"
)

// oneProvider is what most of these tests pass for BuildMenu's providers
// parameter: with exactly one, "Add account…" stays a plain, single item
// (see TestBuildMenuAddAccountRowByProviderCount for the 0/many cases),
// which is what every test below not specifically about that row's own
// shape assumes.
var oneProvider = []ProviderChoice{{Provider: "claude", Label: "Claude"}}

func TestBuildMenuEmptyAccounts(t *testing.T) {
	m := BuildMenu(nil, DefaultThresholds, Actions{}, oneProvider)
	if len(m.Items) == 0 {
		t.Fatal("BuildMenu(nil, ...): want at least the placeholder + action rows")
	}
	if m.Items[0].Label != "No accounts yet" || !m.Items[0].Disabled {
		t.Fatalf("first row = %+v, want a disabled placeholder", m.Items[0])
	}
}

func TestBuildMenuAccountRows(t *testing.T) {
	accounts := []AccountStatus{
		{AccountID: "acct-1", Label: "work@example.com", Snapshot: snapshot(
			&quotapb.QuotaWindow{Label: "session", Used: 45, Limit: 100},
			&quotapb.QuotaWindow{Label: "weekly", Used: 78, Limit: 100},
		)},
		{AccountID: "acct-2", Err: errors.New("session expired")},
		{AccountID: "acct-3", Label: "no-poll@example.com"},
	}
	m := BuildMenu(accounts, DefaultThresholds, Actions{}, oneProvider)

	if got := m.Items[0].Label; !strings.Contains(got, "work@example.com") || !strings.Contains(got, "session 45%") || !strings.Contains(got, "weekly 78%") {
		t.Fatalf("row 0 = %q, want it to name the account and both windows' percentages", got)
	}
	if !m.Items[0].Disabled {
		t.Fatal("an account row must be disabled: a menu bar is for glancing, not clicking a row to do nothing")
	}
	if got := m.Items[1].Label; !strings.Contains(got, "acct-2") || !strings.Contains(got, "session expired") {
		t.Fatalf("row 1 = %q, want the account id and its error", got)
	}
	if got := m.Items[2].Label; !strings.Contains(got, "no-poll@example.com") || !strings.Contains(got, "not polled yet") {
		t.Fatalf("row 2 = %q, want the label and \"not polled yet\"", got)
	}
}

func TestBuildMenuNoUsageData(t *testing.T) {
	accounts := []AccountStatus{{AccountID: "a", Snapshot: snapshot()}}
	m := BuildMenu(accounts, DefaultThresholds, Actions{}, oneProvider)
	if got := m.Items[0].Label; !strings.Contains(got, "no usage data") {
		t.Fatalf("row = %q, want \"no usage data\" for a snapshot with zero windows", got)
	}
}

func TestBuildMenuWindowWithNoLimitIsNA(t *testing.T) {
	accounts := []AccountStatus{{AccountID: "a", Snapshot: snapshot(window(5, 0))}}
	m := BuildMenu(accounts, DefaultThresholds, Actions{}, oneProvider)
	if got := m.Items[0].Label; !strings.Contains(got, "n/a") {
		t.Fatalf("row = %q, want \"n/a\" for a window with no reported limit", got)
	}
}

func TestBuildMenuFallsBackToAccountIDWithNoLabel(t *testing.T) {
	accounts := []AccountStatus{{AccountID: "bare-id", Snapshot: snapshot(window(1, 100))}}
	m := BuildMenu(accounts, DefaultThresholds, Actions{}, oneProvider)
	if got := m.Items[0].Label; !strings.Contains(got, "bare-id") {
		t.Fatalf("row = %q, want it to fall back to AccountID when Label is empty", got)
	}
}

func TestBuildMenuTrailingActionsAlwaysPresent(t *testing.T) {
	m := BuildMenu(nil, DefaultThresholds, Actions{}, oneProvider)
	last := m.Items[len(m.Items)-1]
	if last.Label != "Quit" {
		t.Fatalf("last row = %q, want \"Quit\"", last.Label)
	}
	lockIdx := len(m.Items) - 2
	if m.Items[lockIdx].Label != "Lock now" {
		t.Fatalf("second-to-last row = %q, want \"Lock now\"", m.Items[lockIdx].Label)
	}
	addIdx := len(m.Items) - 3
	if m.Items[addIdx].Label != "Add account…" {
		t.Fatalf("third-to-last row = %q, want \"Add account…\"", m.Items[addIdx].Label)
	}
}

func TestBuildMenuActionsFireTheGivenCallbacks(t *testing.T) {
	var addCalled, lockCalled, quitCalled bool
	var gotProvider string
	m := BuildMenu(nil, DefaultThresholds, Actions{
		AddAccount: func(p string) { addCalled = true; gotProvider = p },
		LockNow:    func() { lockCalled = true },
		Quit:       func() { quitCalled = true },
	}, oneProvider)
	for _, it := range m.Items {
		switch it.Label {
		case "Add account…", "Lock now", "Quit":
			it.Activate()
		}
	}
	if !addCalled || !lockCalled || !quitCalled {
		t.Fatalf("addCalled=%v lockCalled=%v quitCalled=%v, want all true", addCalled, lockCalled, quitCalled)
	}
	if gotProvider != "claude" {
		t.Fatalf("AddAccount got provider %q, want %q", gotProvider, "claude")
	}
}

// TestBuildMenuNilActionsDoNotPanic covers addAccountItem's own nil-safe
// fallback (mirroring orNoop for the other rows): a caller that hasn't
// wired a callback yet gets an inert row, not a crash on click.
func TestBuildMenuNilActionsDoNotPanic(t *testing.T) {
	m := BuildMenu(nil, DefaultThresholds, Actions{}, oneProvider)
	for _, it := range m.Items {
		if it.OnClick != nil {
			it.Activate() // must not panic
		}
	}
}

// TestBuildMenuAddAccountRowByProviderCount is the actual unit-level proof
// of BuildMenu's own branching on len(providers) — Loop's own tests (see
// app/loop_test.go's TestLoopDrivenByRealMenuItemActivate and its
// _MultipleProviders sibling) prove the SAME thing end-to-end through a
// real Activate(), but the exact row shape (disabled / plain / submenu)
// belongs here, next to the code that decides it.
func TestBuildMenuAddAccountRowByProviderCount(t *testing.T) {
	none := BuildMenu(nil, DefaultThresholds, Actions{}, nil)
	row := findMenuItem(t, none, func(it *tray.MenuItem) bool {
		return strings.HasPrefix(it.Label, "Add account")
	})
	if !row.Disabled || row.Submenu != nil {
		t.Fatalf("0 providers: row = %+v, want a disabled plain row", row)
	}

	one := BuildMenu(nil, DefaultThresholds, Actions{}, oneProvider)
	row = findMenuItem(t, one, func(it *tray.MenuItem) bool { return it.Label == "Add account…" })
	if row.Disabled || row.Submenu != nil {
		t.Fatalf("1 provider: row = %+v, want a plain, enabled, non-submenu row", row)
	}

	many := BuildMenu(nil, DefaultThresholds, Actions{}, []ProviderChoice{
		{Provider: "claude", Label: "Claude"},
		{Provider: "chatgpt", Label: "ChatGPT"},
	})
	row = findMenuItem(t, many, func(it *tray.MenuItem) bool { return it.Label == "Add account…" })
	if row.Submenu == nil {
		t.Fatal("2 providers: row has no Submenu, want one row per provider")
	}
	if len(row.Submenu.Items) != 2 || row.Submenu.Items[0].Label != "Claude" || row.Submenu.Items[1].Label != "ChatGPT" {
		t.Fatalf("2 providers: submenu items = %+v, want [Claude ChatGPT] in the given order", row.Submenu.Items)
	}
}

func findMenuItem(t *testing.T, m *tray.Menu, match func(*tray.MenuItem) bool) *tray.MenuItem {
	t.Helper()
	for _, it := range m.Items {
		if match(it) {
			return it
		}
	}
	t.Fatal("no matching menu row")
	return nil
}
