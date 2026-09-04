// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package app

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-aiquota/tray/menubar"
	"github.com/go-widgets/tray"
)

// fakeTray mimics the REAL tray backend's Hold/Release contract closely
// enough for Loop's own usage pattern: Hold blocks until Release, and — per
// go-widgets/tray's darwin backend doc comment ("This tray is run,
// released and run again") — a FRESH Hold after that blocks again until
// the NEXT Release, not forever-closed like a one-shot channel.
type fakeTray struct {
	mu      sync.Mutex
	gate    chan struct{}
	entered chan struct{} // signaled (non-blocking) each time Hold starts waiting
	err     error         // returned by the next Hold, if set
}

func newFakeTray() *fakeTray {
	return &fakeTray{gate: make(chan struct{}), entered: make(chan struct{}, 1)}
}

func (f *fakeTray) Hold() error {
	f.mu.Lock()
	gate := f.gate
	err := f.err
	f.mu.Unlock()
	select {
	case f.entered <- struct{}{}:
	default:
	}
	<-gate
	return err
}

func (f *fakeTray) Release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.gate:
		// Already released since the last Hold — Loop never does this, but
		// closing an already-closed channel would panic, so guard it.
	default:
		close(f.gate)
	}
	f.gate = make(chan struct{})
}

func waitEntered(t *testing.T, f *fakeTray) {
	t.Helper()
	select {
	case <-f.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Loop never re-entered Hold")
	}
}

func TestLoopAddAccountThenContinues(t *testing.T) {
	fake := newFakeTray()
	actions := make(chan Action, 1)
	addCh := make(chan string, 1)
	go Loop(fake, actions, Handlers{OnAddAccount: func(provider string) { addCh <- provider }})

	waitEntered(t, fake)
	actions <- Action{Kind: ActionAddAccount, Provider: "claude"}
	select {
	case got := <-addCh:
		if got != "claude" {
			t.Fatalf("OnAddAccount got provider %q, want %q", got, "claude")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnAddAccount was not called")
	}
	// Loop must have re-held the tray for the next action rather than
	// stopping — ActionAddAccount is not ActionQuit.
	waitEntered(t, fake)
}

func TestLoopLockNowThenContinues(t *testing.T) {
	fake := newFakeTray()
	actions := make(chan Action, 1)
	lockCh := make(chan struct{}, 1)
	go Loop(fake, actions, Handlers{OnLockNow: func() { lockCh <- struct{}{} }})

	waitEntered(t, fake)
	actions <- Action{Kind: ActionLockNow}
	select {
	case <-lockCh:
	case <-time.After(2 * time.Second):
		t.Fatal("OnLockNow was not called")
	}
	waitEntered(t, fake)
}

func TestLoopQuitReturns(t *testing.T) {
	fake := newFakeTray()
	actions := make(chan Action, 1)
	quitCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		Loop(fake, actions, Handlers{OnQuit: func() { close(quitCh) }})
		close(done)
	}()

	waitEntered(t, fake)
	actions <- Action{Kind: ActionQuit}
	select {
	case <-quitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("OnQuit was not called")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Loop did not return after ActionQuit")
	}
}

func TestLoopNilHandlersDoNotPanic(t *testing.T) {
	fake := newFakeTray()
	actions := make(chan Action, 3)
	done := make(chan struct{})
	go func() {
		Loop(fake, actions, Handlers{})
		close(done)
	}()

	waitEntered(t, fake)
	actions <- Action{Kind: ActionAddAccount, Provider: "claude"}
	waitEntered(t, fake)
	actions <- Action{Kind: ActionLockNow}
	waitEntered(t, fake)
	actions <- Action{Kind: ActionQuit}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Loop did not return after ActionQuit")
	}
}

func TestLoopHoldErrorStillWaitsForTheNextAction(t *testing.T) {
	fake := newFakeTray()
	fake.err = errors.New("no platform backend")
	actions := make(chan Action, 1)
	var gotErr error
	errCh := make(chan struct{}, 1)
	quitCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		Loop(fake, actions, Handlers{
			OnHoldError: func(err error) { gotErr = err; errCh <- struct{}{} },
			OnQuit:      func() { close(quitCh) },
		})
		close(done)
	}()

	waitEntered(t, fake)
	// The fake's Hold returns the error immediately (it does not block on
	// a real event loop the way a real backend without one still would),
	// so Release the gate to let it return.
	fake.Release()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("OnHoldError was not called")
	}
	if gotErr == nil || gotErr.Error() != "no platform backend" {
		t.Fatalf("OnHoldError got %v, want the Hold error", gotErr)
	}
	// A Hold error must not stop the loop: Lock/Quit should still work.
	actions <- Action{Kind: ActionQuit}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Loop did not return after ActionQuit, following a Hold error")
	}
}

// TestLoopDrivenByRealMenuItemActivate is the load-bearing test: it builds
// the REAL menu construction path (menubar.BuildMenu) with its Actions
// wired exactly as cmd/aiquota-tray/main.go wires them, then drives the
// whole thing by calling MenuItem.Activate() directly — the exact call
// go-widgets/tray's own native macOS backend makes when a person clicks a
// row (see native_shared.go: "items[tag].Activate()"). No mouse, no
// synthetic OS input, no screen coordinates: this is the SAME Go call a
// real click resolves to, not an approximation of one.
func TestLoopDrivenByRealMenuItemActivate(t *testing.T) {
	fake := newFakeTray()
	actions := make(chan Action, 1)
	providers := []menubar.ProviderChoice{{Provider: "claude", Label: "Claude"}}
	menu := menubar.BuildMenu(nil, menubar.DefaultThresholds, menubar.Actions{
		AddAccount: func(p string) { actions <- Action{Kind: ActionAddAccount, Provider: p} },
		LockNow:    func() { actions <- Action{Kind: ActionLockNow} },
		Quit:       func() { actions <- Action{Kind: ActionQuit} },
	}, providers)

	addCh := make(chan string, 1)
	lockCh := make(chan struct{}, 1)
	quitCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		Loop(fake, actions, Handlers{
			OnAddAccount: func(provider string) { addCh <- provider },
			OnLockNow:    func() { lockCh <- struct{}{} },
			OnQuit:       func() { close(quitCh) },
		})
		close(done)
	}()

	waitEntered(t, fake)
	findRow(t, menu, "Add account…").Activate()
	select {
	case got := <-addCh:
		if got != "claude" {
			t.Fatalf("OnAddAccount got provider %q, want %q", got, "claude")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("activating \"Add account…\" did not reach OnAddAccount")
	}

	waitEntered(t, fake)
	findRow(t, menu, "Lock now").Activate()
	select {
	case <-lockCh:
	case <-time.After(2 * time.Second):
		t.Fatal("activating \"Lock now\" did not reach OnLockNow")
	}

	waitEntered(t, fake)
	findRow(t, menu, "Quit").Activate()
	select {
	case <-quitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("activating \"Quit\" did not reach OnQuit")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Loop did not return after \"Quit\" was activated")
	}
}

// TestLoopDrivenByRealMenuItemActivate_MultipleProviders proves the actual
// point of ProviderChoice: with more than one provider plugin installed,
// "Add account…" becomes a submenu, and activating a SPECIFIC provider's
// row — not just the top-level label — is what reaches OnAddAccount, with
// the right provider name attached. Same real-menu, no-mouse Activate()
// discipline as the single-provider test above.
func TestLoopDrivenByRealMenuItemActivate_MultipleProviders(t *testing.T) {
	fake := newFakeTray()
	actions := make(chan Action, 1)
	providers := []menubar.ProviderChoice{
		{Provider: "chatgpt", Label: "ChatGPT"},
		{Provider: "claude", Label: "Claude"},
	}
	menu := menubar.BuildMenu(nil, menubar.DefaultThresholds, menubar.Actions{
		AddAccount: func(p string) { actions <- Action{Kind: ActionAddAccount, Provider: p} },
	}, providers)

	addCh := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		Loop(fake, actions, Handlers{OnAddAccount: func(provider string) { addCh <- provider }})
		close(done)
	}()
	t.Cleanup(func() {
		actions <- Action{Kind: ActionQuit}
		<-done
	})

	waitEntered(t, fake)
	sub := findRow(t, menu, "Add account…")
	if sub.Submenu == nil {
		t.Fatal(`"Add account…" is not a submenu with two providers installed`)
	}
	findRow(t, sub.Submenu, "Claude").Activate()
	select {
	case got := <-addCh:
		if got != "claude" {
			t.Fatalf("OnAddAccount got provider %q, want %q", got, "claude")
		}
	case <-time.After(2 * time.Second):
		t.Fatal(`activating the "Claude" submenu row did not reach OnAddAccount`)
	}
}

func findRow(t *testing.T, m *tray.Menu, label string) *tray.MenuItem {
	t.Helper()
	for _, it := range m.Items {
		if it.Label == label {
			return it
		}
	}
	t.Fatalf("no menu row labeled %q", label)
	return nil
}
