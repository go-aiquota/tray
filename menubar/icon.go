// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"bytes"
	"fmt"
	"image"
	"image/png"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// IconPx is the icon's side length, in pixels — twice a typical menu bar's
// own height, so it stays sharp on a 2x display (the toolkit scales it down
// to fit, per DrawIconoir's own box-fitting).
const IconPx = 44

// glyphInk is a neutral mid-gray for the base glyph, chosen to read
// reasonably on both a light and a dark menu bar without querying the
// platform's current appearance (a real per-appearance recolor, as
// go-xrkit/desk's traydesk.go does, is a reasonable follow-up — skipped
// here since every state below already carries a colored dot, which
// forfeits automatic template recoloring anyway; see
// [[tray-icon-state-and-stencil]]).
var glyphInk = toolkit.RGB(0x8A, 0x8A, 0x8A)

// dotInk is the status dot's color per severity. SeverityUnknown gets no
// dot at all (see Icon) rather than a color, since "nothing known yet" is
// not a status to alarm or reassure about.
var dotInk = map[Severity]toolkit.RGBA{
	SeverityOK:       toolkit.RGB(0x34, 0xC7, 0x59), // green
	SeverityWarning:  toolkit.RGB(0xE0, 0xA5, 0x2A), // amber
	SeverityCritical: toolkit.RGB(0xE0, 0x3B, 0x3B), // red
	SeverityError:    toolkit.RGB(0x8A, 0x2A, 0xE0), // purple — deliberately NOT red: a poll failure (re-auth needed) is a different problem than "quota nearly gone", and conflating them with the same color would send the user to wait out a critical-quota state when what they actually need to do is re-authenticate an account.
}

// Icon renders the tray icon at px pixels for severity, as PNG bytes: an
// "activity" glyph (go-icons/iconoir, drawn through the toolkit per
// [[feedback-icons-through-toolkit]] rather than hand-drawn) with a status
// dot layered on the lower-right, present for every severity except
// SeverityUnknown (no dot — see dotInk's doc comment).
func Icon(px int, severity Severity) ([]byte, error) {
	if px <= 0 {
		return nil, fmt.Errorf("menubar: an icon of %d pixels", px)
	}
	buf := make([]byte, px*px*4)
	p := painter.NewPixelPainterBGRA(buf, px, px)
	box := toolkit.Rect{W: px, H: px}
	toolkit.DrawIconoir(p, box, "activity", glyphInk)

	if ink, ok := dotInk[severity]; ok {
		d := px / 2
		if d < 6 {
			d = min(6, px)
		}
		dot := toolkit.Rect{X: px - d, Y: px - d, W: d, H: d}
		toolkit.DrawIconDot(p, dot, ink)
	}

	// The painter writes BGRA; image/png wants (N)RGBA.
	pix := make([]byte, len(buf))
	for i := 0; i+3 < len(buf); i += 4 {
		pix[i], pix[i+1], pix[i+2], pix[i+3] = buf[i+2], buf[i+1], buf[i], buf[i+3]
	}
	img := image.NewNRGBA(image.Rect(0, 0, px, px))
	copy(img.Pix, pix)

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Icons builds the go-widgets/tray.Icons map every Severity needs for
// tray.BindIcon — one still frame per state (no animation in v1: a
// spinning/pulsing icon while polling was not asked for and would be
// speculative). A severity whose Icon call fails is simply absent from the
// map, matching tray.BindIcon's own documented behavior for an unmapped
// state (leaves the icon alone rather than blanking it).
func Icons(px int) map[Severity][][]byte {
	out := map[Severity][][]byte{}
	for _, s := range []Severity{SeverityUnknown, SeverityOK, SeverityWarning, SeverityCritical, SeverityError} {
		if b, err := Icon(px, s); err == nil {
			out[s] = [][]byte{b}
		}
	}
	return out
}
