// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package onboarding drives the embedded, isolated per-account login flow:
// one throwaway go-webengine/engine.LiveDocument per account, rendered into
// a fixed-size window and driven by real mouse/keyboard input translated
// into the same focus/type/click sequence a real browser would deliver.
// Window implements github.com/go-widgets/application's Handler, so a host
// opens a real platform window over it via application.Run — but every
// method here is independently testable without one (see window_test.go),
// since Handler itself is just plain Go methods over an RGBA buffer.
package onboarding

import (
	"context"
	"image"
	"net/url"

	"github.com/go-webengine/browserproxy"
	"github.com/go-webengine/engine"
	"github.com/go-webengine/engine/dom"
)

// Window is one account's isolated onboarding session: its own engine, its
// own cookie jar (never the user's real browser's, never shared with
// another account's Window), open on exactly one login page until the
// caller captures its cookies and Closes it. Not safe for concurrent use —
// like LiveDocument, one goroutine drives one Window at a time (a real
// platform window's event loop already serializes input this way).
type Window struct {
	eng  *engine.Engine
	live *engine.LiveDocument

	w, h    int // fixed viewport size in device pixels — no live resize in v1
	scrollY int
	focused *dom.Node // tracked here because LiveDocument does not expose its own focus state; every focus change in this file goes through here

	changed bool // Frame()'s damage flag: set by every method that can change what's on screen, cleared when Frame() reports it
}

// Open starts an isolated login session at loginURL: a fresh engine whose
// HTTP client is SSRF-guarded (browserproxy.GuardedClient — the same guard
// the wasmdesk remote-browser proxy uses, reused here per its own README
// rather than re-implemented) and whose cookie jar belongs to this Window
// alone.
func Open(ctx context.Context, loginURL string, w, h int) (*Window, error) {
	eng := engine.New()
	eng.Client = browserproxy.GuardedClient(eng.Client)
	return openWith(ctx, eng, loginURL, w, h)
}

// openWith is Open's guts, taking an already-constructed engine — the seam
// window_test.go uses to test against a local httptest server, which the
// SSRF guard correctly refuses (loopback is exactly what it exists to
// block for a REAL provider login). Every other Window behavior is
// identical either way; only which engine/client backs it differs.
func openWith(ctx context.Context, eng *engine.Engine, loginURL string, w, h int) (*Window, error) {
	live, err := eng.Open(ctx, loginURL, image.Rect(0, 0, w, h))
	if err != nil {
		return nil, err
	}
	return &Window{eng: eng, live: live, w: w, h: h, changed: true}, nil
}

// Close releases the underlying LiveDocument's session. Idempotent (so does
// LiveDocument.Close).
func (win *Window) Close() { win.live.Close() }

// Cookies returns this Window's isolated jar's current cookies for domain,
// as name->value — exactly the shape go-aiquota/proto's Credential map
// takes. Called once login completes (the caller decides "completes" —
// e.g. the provider's declared CookieDomain now has a session cookie, or
// the page navigated somewhere the caller recognizes as post-login).
func (win *Window) Cookies(domain string) map[string]string {
	out := map[string]string{}
	if win.eng.Client == nil || win.eng.Client.Jar == nil {
		return out
	}
	for _, c := range win.eng.Client.Jar.Cookies(&url.URL{Scheme: "https", Host: domain, Path: "/"}) {
		out[c.Name] = c.Value
	}
	return out
}

// CurrentURL returns the page currently loaded in the window — what a
// caller polls to notice "login completed, we navigated to the app" rather
// than only checking for a cookie.
func (win *Window) CurrentURL() string { return win.live.Document().URL }
