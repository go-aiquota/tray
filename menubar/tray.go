// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"time"

	"github.com/go-widgets/mvvm"
	"github.com/go-widgets/tray"
)

// Tooltip is the tray item's hover text.
const Tooltip = "AI quota"

// trayBackend is the seam: nil means the platform's own, which is what a
// program wants and what a test cannot have (a menu bar is one per machine).
// It must be set BEFORE Open, not after: Open hands the tray straight to
// tray.BindIcon, which starts a goroutine that calls back into the backend
// immediately — swapping the backend on the *tray.Tray post-construction,
// from a second goroutine, would race that callback against the swap itself
// (go-widgets/tray.Tray.refresh reads its backend field without a lock,
// matching every other field it treats as fixed once Run/Attach starts).
var trayBackend = func() tray.Backend { return nil }

// Tray is go-aiquota's menu-bar item and the two ways it can live, mirroring
// go-xrkit/desk's traydesk.go: a tray icon needs a running platform loop to
// be openable at all, and a program either has none yet (Hold, blocking) or
// already owns one because a window is up (Attach, non-blocking).
type Tray struct {
	t     *tray.Tray
	state *mvvm.Observable[Severity]
	stop  func()
}

// Open builds the tray item, showing state's current severity and menu, and
// makes the icon follow state as it changes. It does not itself start any
// event loop — call Hold or Attach for that.
func Open(state *mvvm.Observable[Severity], menu *tray.Menu) (*Tray, error) {
	icon, err := Icon(IconPx, state.Get())
	if err != nil {
		return nil, err
	}
	t := tray.New(icon)
	if b := trayBackend(); b != nil {
		t = t.WithBackend(b)
	}
	t.SetTooltip(Tooltip)
	t.SetMenu(menu)
	item := &Tray{t: t, state: state}
	item.stop = tray.BindIcon(t, state, Icons(IconPx), time.Second)
	return item, nil
}

// SetMenu replaces the tray's menu (e.g. after the account list changes).
func (t *Tray) SetMenu(m *tray.Menu) { t.t.SetMenu(m) }

// SetVisible shows or hides the control item's own icon without releasing
// the platform loop every per-account item (see AccountItems) Attaches to
// — see go-widgets/tray.Tray.SetVisible's own doc comment for why hiding,
// not closing, is what a caller reaches for once the control item's icon
// has become redundant next to the per-account items it Holds the loop
// for.
func (t *Tray) SetVisible(v bool) { t.t.SetVisible(v) }

// OnReady registers fn to run once this tray's platform loop is actually
// live — Hold has started it, or Attach has joined a host's already-running
// one. It must be set before Hold/Attach.
//
// This is the one moment a caller can safely Attach an ADDITIONAL item
// (see menubar.AccountItems) of their own: Attach's underlying prepare
// step still works before any loop exists (go-widgets/tray's runOnMain
// runs inline on the main thread), but doing it from the goroutine that is
// about to call Hold, before Hold has run at all, races the shared
// NSApplication's very first setup against Hold's own — fn runs
// synchronously on the main thread during that setup instead, after it,
// where the ordering is no longer ambiguous.
func (t *Tray) OnReady(fn func()) *Tray {
	t.t.OnReady(fn)
	return t
}

// Hold runs the item and the platform's main loop, returning when Release is
// called. Use it when nothing else is driving the platform loop yet (no
// window open) — it must be called on the main thread.
func (t *Tray) Hold() error { return t.t.Run() }

// Release stops the loop Hold is running, so the caller can go on to open a
// window (which will drive its own loop) or exit.
func (t *Tray) Release() { t.t.Quit() }

// Attach adds the item to a loop somebody else is already running (e.g. an
// onboarding window's own event loop) and returns at once.
func (t *Tray) Attach() error { return t.t.Attach() }

// Close takes the item away and stops the icon following state.
func (t *Tray) Close() error {
	if t.stop != nil {
		t.stop()
		t.stop = nil
	}
	t.t.Quit()
	return nil
}
