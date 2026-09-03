// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"errors"
	"testing"

	"github.com/go-aiquota/proto/quotapb"
	"github.com/go-widgets/tray"
)

func TestTitleForError(t *testing.T) {
	status := AccountStatus{Err: errors.New("boom")}
	if got := titleFor(status); got != "⚠" {
		t.Fatalf("titleFor(error) = %q, want the warning glyph", got)
	}
}

func TestTitleForNotPolledYet(t *testing.T) {
	if got := titleFor(AccountStatus{}); got != "…" {
		t.Fatalf("titleFor(no snapshot) = %q, want an ellipsis", got)
	}
	status := AccountStatus{Snapshot: &quotapb.QuotaSnapshot{}}
	if got := titleFor(status); got != "…" {
		t.Fatalf("titleFor(empty windows) = %q, want an ellipsis", got)
	}
}

func TestTitleForSessionAndWeekly(t *testing.T) {
	status := AccountStatus{Snapshot: &quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{
		{Label: "session", Used: 46, Limit: 100},
		{Label: "weekly_all", Used: 10, Limit: 100},
		{Label: "weekly_scoped", Used: 0, Limit: 100},
	}}}
	if got := titleFor(status); got != "46%/10%" {
		t.Fatalf("titleFor(session+weekly) = %q, want %q (weekly_scoped excluded)", got, "46%/10%")
	}
}

func TestTitleForNoRecognizedWindow(t *testing.T) {
	status := AccountStatus{Snapshot: &quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{
		{Label: "monthly_credits", Used: 5, Limit: 100},
	}}}
	if got := titleFor(status); got != "…" {
		t.Fatalf("titleFor(no session/weekly window) = %q, want an ellipsis", got)
	}
}

func TestTitleForWindowWithNoLimitIsSkipped(t *testing.T) {
	status := AccountStatus{Snapshot: &quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{
		{Label: "session", Used: 5, Limit: 0},
	}}}
	if got := titleFor(status); got != "…" {
		t.Fatalf("titleFor(session with no limit) = %q, want an ellipsis", got)
	}
}

// fakeAttachableBackend is a Headless that also implements Attach.
// Headless deliberately does NOT (go-widgets/tray's own tests prove a
// non-attacher backend makes Attach return ErrNoBackend), and it exposes
// no way to fake that support itself, so AccountItems' own tests need a
// small wrapper — mirroring go-widgets/tray's own fakeAttachBackend test
// fixture.
type fakeAttachableBackend struct {
	*tray.Headless
}

func newFakeAttachableBackend() *fakeAttachableBackend {
	return &fakeAttachableBackend{Headless: tray.NewHeadless()}
}

func (f *fakeAttachableBackend) Attach(t *tray.Tray) error {
	f.Started = true
	f.Refresh(t)
	return nil
}

func TestAccountItemsSyncCreatesUpdatesAndRemoves(t *testing.T) {
	prev := trayBackend
	var created []*fakeAttachableBackend
	trayBackend = func() tray.Backend {
		b := newFakeAttachableBackend()
		created = append(created, b)
		return b
	}
	t.Cleanup(func() { trayBackend = prev })

	items := NewAccountItems(Actions{}, DefaultThresholds)

	a1 := AccountStatus{AccountID: "a1", Label: "work", Snapshot: snapshot(&quotapb.QuotaWindow{Label: "session", Used: 46, Limit: 100})}
	a2 := AccountStatus{AccountID: "a2", Label: "personal", Snapshot: snapshot(&quotapb.QuotaWindow{Label: "session", Used: 10, Limit: 100})}

	if err := items.Sync([]AccountStatus{a1, a2}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if items.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", items.Len())
	}
	if len(created) != 2 {
		t.Fatalf("backends created = %d, want 2 (one per account)", len(created))
	}
	for _, b := range created {
		if !b.Started {
			t.Error("a created item's backend was never Attach()ed")
		}
	}

	// Update: same accounts, changed data — must reuse the existing items
	// (no new Attach), and reflect the new title.
	a1Updated := AccountStatus{AccountID: "a1", Label: "work", Snapshot: snapshot(&quotapb.QuotaWindow{Label: "session", Used: 90, Limit: 100})}
	if err := items.Sync([]AccountStatus{a1Updated, a2}); err != nil {
		t.Fatalf("Sync (update): %v", err)
	}
	if items.Len() != 2 || len(created) != 2 {
		t.Fatalf("Sync on existing accounts created a NEW item: Len=%d created=%d", items.Len(), len(created))
	}
	if title := created[0].LastTitle; title != "90%" {
		t.Fatalf("a1's item title after update = %q, want %q", title, "90%")
	}

	// Removal: a2 gone — its item must be Closed, a1's must remain.
	if err := items.Sync([]AccountStatus{a1Updated}); err != nil {
		t.Fatalf("Sync (removal): %v", err)
	}
	if items.Len() != 1 {
		t.Fatalf("Len() after removing a2 = %d, want 1", items.Len())
	}

	items.Close()
	if items.Len() != 0 {
		t.Fatalf("Len() after Close = %d, want 0", items.Len())
	}
}

func TestAccountItemsSyncEmptyIsANoop(t *testing.T) {
	prev := trayBackend
	trayBackend = func() tray.Backend { return newFakeAttachableBackend() }
	t.Cleanup(func() { trayBackend = prev })

	items := NewAccountItems(Actions{}, DefaultThresholds)
	if err := items.Sync(nil); err != nil {
		t.Fatalf("Sync(nil): %v, want no error for an empty account list", err)
	}
	if items.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", items.Len())
	}
}

// TestAccountItemsMenuMatchesBuildMenuForOneAccount proves upsert's menu is
// exactly what BuildMenu itself produces for a single-account list — the
// same function the control item uses — so there is no second,
// independently-maintained menu-building path to drift from it.
func TestAccountItemsMenuMatchesBuildMenuForOneAccount(t *testing.T) {
	prev := trayBackend
	var b *fakeAttachableBackend
	trayBackend = func() tray.Backend {
		b = newFakeAttachableBackend()
		return b
	}
	t.Cleanup(func() { trayBackend = prev })

	var addCalled bool
	actions := Actions{AddAccount: func() { addCalled = true }}
	status := AccountStatus{AccountID: "a1", Label: "work", Snapshot: snapshot(&quotapb.QuotaWindow{Label: "session", Used: 46, Limit: 100})}

	items := NewAccountItems(actions, DefaultThresholds)
	if err := items.Sync([]AccountStatus{status}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	want := BuildMenu([]AccountStatus{status}, DefaultThresholds, actions)
	_, _, got := b.Snapshot()
	if len(got.Items) != len(want.Items) {
		t.Fatalf("menu has %d items, want %d (matching BuildMenu's own shape)", len(got.Items), len(want.Items))
	}
	for i := range got.Items {
		if got.Items[i].Label != want.Items[i].Label {
			t.Errorf("item %d label = %q, want %q", i, got.Items[i].Label, want.Items[i].Label)
		}
	}
	// And the item's own menu is genuinely wired to the SAME callbacks, not
	// just equal-looking labels.
	for _, it := range got.Items {
		if it.Label == "Add account…" {
			it.Activate()
		}
	}
	if !addCalled {
		t.Fatal("activating \"Add account…\" on the per-account item's menu did not reach the shared Actions callback")
	}
}
