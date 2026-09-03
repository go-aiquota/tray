// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package app

// Action is what a tray-menu row means once it fires. It arrives the same
// way whether a person actually clicked the row — go-widgets/tray's own
// native backend resolves a click by calling exactly item.Activate() (see
// native_shared.go's click-dispatch table) — or a test calls Activate()
// directly with no mouse, no OS event, and no window server involved at
// all: both routes end up running the identical Go closure a
// menubar.Actions field was given, which is what makes Activate() a real
// verification tool rather than an approximation of one.
type Action int

const (
	ActionNone Action = iota
	ActionAddAccount
	ActionLockNow
	ActionQuit
)

// TrayController is the subset of menubar.Tray that Loop drives: hold the
// platform run loop until something releases it, and give it up on demand.
// As an interface rather than *menubar.Tray directly, a test can supply a
// fake that never touches AppKit/Win32/DBus at all.
type TrayController interface {
	Hold() error
	Release()
}

// Handlers are what Loop does for each action. A nil field is a no-op —
// the same convention menubar.Actions uses, for the same reason: a caller
// that hasn't wired one yet gets nothing, not a crash.
type Handlers struct {
	OnAddAccount func()
	OnLockNow    func()
	OnQuit       func()
	// OnHoldError is called if item.Hold itself fails (e.g. no platform
	// backend). Loop does not stop when this happens: a tray that cannot
	// hold a loop still has a Lock/Quit worth honoring if one somehow
	// arrives, and refusing to wait would busy-loop instead.
	OnHoldError func(error)
}

// Loop runs the tray's Hold/Release/action cycle, mirroring
// go-xrkit/desk/traydesk.go's own precedent for the one hard problem a
// tray icon has: it needs a running platform loop to be usable at all, and
// only one thing can drive that loop at a time. Loop holds it (item.Hold)
// until an action arrives on actions, releases it (item.Release) so the
// caller is free to drive something else — opening a window, which wants
// the main thread to itself — runs the matching Handlers field, and
// re-holds for the next one. It returns when ActionQuit arrives.
func Loop(item TrayController, actions <-chan Action, h Handlers) {
	for {
		got := ActionNone
		done := make(chan struct{})
		go func() {
			defer close(done)
			got = <-actions
			item.Release()
		}()
		if err := item.Hold(); err != nil && h.OnHoldError != nil {
			h.OnHoldError(err)
		}
		<-done

		switch got {
		case ActionQuit:
			if h.OnQuit != nil {
				h.OnQuit()
			}
			return
		case ActionLockNow:
			if h.OnLockNow != nil {
				h.OnLockNow()
			}
		case ActionAddAccount:
			if h.OnAddAccount != nil {
				h.OnAddAccount()
			}
		}
	}
}
