// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"bytes"
	"testing"
	"time"

	"github.com/go-widgets/mvvm"
	"github.com/go-widgets/tray"
)

// openHeadless sets trayBackend to a fresh Headless before calling Open, so
// BindIcon's own animator goroutine (started inside Open, before a caller
// gets any chance to swap the backend) targets the headless backend from
// its very first write — see trayBackend's doc comment for why swapping the
// backend AFTER Open would race that goroutine.
func openHeadless(t *testing.T, state *mvvm.Observable[Severity], menu *tray.Menu) (*Tray, *tray.Headless) {
	t.Helper()
	h := tray.NewHeadless()
	prev := trayBackend
	trayBackend = func() tray.Backend { return h }
	t.Cleanup(func() { trayBackend = prev })

	item, err := Open(state, menu)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return item, h
}

func TestOpenAndHoldRunsUntilRelease(t *testing.T) {
	state := mvvm.NewObservable(SeverityOK)
	item, h := openHeadless(t, state, tray.NewMenu())

	done := make(chan error, 1)
	go func() { done <- item.Hold() }()

	waitUntilStarted(t, h)

	item.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Hold: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Release did not unblock Hold")
	}
}

func TestOpenIconFollowsState(t *testing.T) {
	state := mvvm.NewObservable(SeverityOK)
	item, h := openHeadless(t, state, tray.NewMenu())
	t.Cleanup(func() { _ = item.Close() })

	go func() { _ = item.Hold() }()
	waitUntilStarted(t, h)

	okIcon, _, _ := h.Snapshot()

	state.Set(SeverityCritical)
	waitUntil(t, func() bool {
		icon, _, _ := h.Snapshot()
		return !bytes.Equal(icon, okIcon)
	})
}

func TestOpenRejectsBrokenIconSize(t *testing.T) {
	// Severity values not in dotInk still render fine (SeverityUnknown has
	// no dot) — Open only fails when Icon itself does, which needs px<=0.
	// IconPx is a package constant, so exercise Icon's own error path
	// directly instead (Open has no seam to inject a bad size); this test
	// documents that Open's error return IS reachable in principle, via
	// Icon's contract, without duplicating icon_test.go's coverage of it.
	if _, err := Icon(0, SeverityOK); err == nil {
		t.Fatal("Icon(0, ...): want an error, so Open's own error path is real, not dead")
	}
}

func TestSetMenuReplacesTheMenu(t *testing.T) {
	state := mvvm.NewObservable(SeverityOK)
	item, h := openHeadless(t, state, tray.NewMenu())
	t.Cleanup(func() { _ = item.Close() })

	go func() { _ = item.Hold() }()
	waitUntilStarted(t, h)

	m := tray.NewMenu().Add(tray.Item("only row", func() {}))
	item.SetMenu(m)
	waitUntil(t, func() bool {
		_, _, gotMenu := h.Snapshot()
		return gotMenu != nil && len(gotMenu.Items) == 1 && gotMenu.Items[0].Label == "only row"
	})
}

func TestOnReadyFiresOnceHoldStarts(t *testing.T) {
	state := mvvm.NewObservable(SeverityOK)
	item, h := openHeadless(t, state, tray.NewMenu())
	t.Cleanup(func() { _ = item.Close() })

	ready := make(chan struct{}, 1)
	item.OnReady(func() { close(ready) })

	go func() { _ = item.Hold() }()
	waitUntilStarted(t, h)

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("OnReady never fired after Hold started")
	}
}

func TestAttachWithoutAttachCapableBackendErrors(t *testing.T) {
	state := mvvm.NewObservable(SeverityOK)
	item, _ := openHeadless(t, state, tray.NewMenu()) // Headless does not implement attaching
	if err := item.Attach(); err != tray.ErrNoBackend {
		t.Fatalf("Attach() with a non-attaching backend = %v, want %v", err, tray.ErrNoBackend)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	state := mvvm.NewObservable(SeverityOK)
	item, _ := openHeadless(t, state, tray.NewMenu())
	go func() { _ = item.Hold() }()

	if err := item.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := item.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was never met")
}

// waitUntilStarted waits for h.Run to have taken hold, without racing it: h's
// exported fields (Started included) are documented safe to read directly
// only while nothing can refresh concurrently, which Hold's own goroutine
// violates the instant it starts. Snapshot() takes h's lock, and Run sets
// Started and takes its first snapshot inside that SAME critical section
// (headless.go's Run), so a non-nil icon from Snapshot() happens-after
// Started was set — this is the one race-free way to observe it.
func waitUntilStarted(t *testing.T, h *tray.Headless) {
	t.Helper()
	waitUntil(t, func() bool {
		icon, _, _ := h.Snapshot()
		return icon != nil
	})
}
