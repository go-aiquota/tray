// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package onboarding

import (
	"context"
	"image"
	"image/draw"
	"strings"

	"github.com/go-webengine/engine/dom"
)

// Frame implements github.com/go-widgets/application.Handler: it re-renders
// the live page and returns the current w×h device-pixel slice at the
// window's scroll offset, and whether anything changed since the last call.
func (win *Window) Frame() (buf []byte, w, h int, changed bool) {
	img, _, err := win.live.Frame()
	if err != nil {
		return nil, win.w, win.h, false
	}
	slice := win.sliceViewport(img)
	changed, win.changed = win.changed, false
	return slice.Pix, win.w, win.h, changed
}

// sliceViewport crops full to the window's w×h at the current scroll
// offset, padding with white where the page is shorter than the viewport —
// the same convention go-webengine/browserproxy's own FrameSlice uses, so a
// page never leaves an uninitialized (black/garbage) region on screen.
func (win *Window) sliceViewport(full *image.RGBA) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, win.w, win.h))
	for i := range out.Pix {
		out.Pix[i] = 255
	}
	if full == nil {
		return out
	}
	src := image.Rect(0, win.scrollY, win.w, win.scrollY+win.h).Intersect(full.Bounds())
	if src.Empty() {
		return out
	}
	draw.Draw(out, image.Rect(0, 0, src.Dx(), src.Dy()), full, image.Pt(src.Min.X, src.Min.Y), draw.Src)
	return out
}

// Resize is a no-op in v1: the onboarding window is opened at a fixed size
// (Open's w/h) and does not support live resize — not a capability the
// login flow needs, so not built speculatively.
func (win *Window) Resize(int, int, float64) {}

// MouseDown resolves the click point (adjusted for scroll) against the
// current element index and synthesizes a real click there, tracking the
// clicked element as focused (Key routes to it) — mirroring a real
// browser's own "clicking a field focuses it" behavior, which
// LiveDocument.Click already does internally; this just remembers WHICH
// node for Key's benefit, since LiveDocument does not expose its own focus
// state.
//
// Click alone only fires the click EVENT — a real browser's default action
// for clicking a submit control inside a form is to submit it, which
// LiveDocument.Click does not do on its own (Submit is its own explicit
// method, by design: not every click is a submit). So a click that resolved
// to a submit control and was NOT preventDefault()'d additionally submits
// its nearest ancestor form; a real navigation swaps this Window onto the
// LiveDocument Submit returns, resetting scroll and focus (the old page's
// state no longer applies) exactly like a real page load does.
func (win *Window) MouseDown(x, y int) {
	n, ok := win.live.ElementAt(image.Pt(x, y+win.scrollY))
	if !ok {
		return
	}
	win.focused = n
	ctx := context.Background()
	prevented, _, _, err := win.live.Click(ctx, n)
	if err == nil && !prevented {
		if form := submitFormFor(n); form != nil {
			if next, navigated, err := win.live.Submit(ctx, form); err == nil && navigated {
				win.live = next
				win.scrollY = 0
				win.focused = nil
			}
		}
	}
	win.changed = true
}

// submitFormFor returns n's nearest ancestor <form> if n is a submit
// control, else nil. A <button> with no explicit type defaults to
// type=submit per the HTML spec (the common real-world case — most
// buttons in a real form carry no type attribute at all).
func submitFormFor(n *dom.Node) *dom.Node {
	isSubmit := false
	switch n.Tag {
	case "button":
		t := strings.ToLower(n.Attr["type"])
		isSubmit = t == "" || t == "submit"
	case "input":
		isSubmit = strings.EqualFold(n.Attr["type"], "submit")
	}
	if !isSubmit {
		return nil
	}
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == dom.Element && p.Tag == "form" {
			return p
		}
	}
	return nil
}

// MouseUp and MouseMove are no-ops in v1: MouseDown alone already
// synthesizes a full click (LiveDocument.Click composes mousedown/mouseup/
// click itself), and this window has nothing that reacts to hover or drag.
func (win *Window) MouseUp(int, int)   {}
func (win *Window) MouseMove(int, int) {}

// Scroll adjusts the viewport offset, clamped to the page's current
// content height (re-measured each call, since typing/clicking can change
// page height).
func (win *Window) Scroll(dy int) {
	_, info, err := win.live.Frame()
	max := 0
	if err == nil && info.ContentHeight > win.h {
		max = info.ContentHeight - win.h
	}
	win.scrollY = clampInt(win.scrollY+dy, 0, max)
	win.changed = true
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Key routes a keystroke to whatever MouseDown last focused: a printable
// rune is typed, "Backspace" deletes, any other named key (Enter, Tab, …)
// is dispatched as a bare keydown/keyup for the page's own script to react
// to (see LiveDocument.KeyDown's doc comment — no Tab-order traversal or
// implicit Enter-submit is modeled). A keystroke before any field has been
// clicked is dropped — there's nothing to type into yet, matching a real
// browser (nothing is focused, nothing receives the key).
func (win *Window) Key(name string, r rune) {
	if win.focused == nil {
		return
	}
	ctx := context.Background()
	switch {
	case name == "Backspace":
		_, _, _ = win.live.Backspace(ctx, win.focused)
	case name == "" && r != 0:
		_, _, _ = win.live.Type(ctx, win.focused, string(r))
	case name != "":
		_, _, _, _ = win.live.KeyDown(ctx, win.focused, name)
	default:
		return
	}
	win.changed = true
}

// Focused returns the node MouseDown last focused, if any — exported for a
// host that wants to show its own chrome around the embedded page (e.g. an
// address-bar-less title reflecting CurrentURL) without reaching into
// unexported state.
func (win *Window) Focused() (*dom.Node, bool) { return win.focused, win.focused != nil }
