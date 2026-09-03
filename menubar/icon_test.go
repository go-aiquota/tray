// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"bytes"
	"image/png"
	"testing"
)

func TestIconEveryStateEncodes(t *testing.T) {
	for _, s := range []Severity{SeverityUnknown, SeverityOK, SeverityWarning, SeverityCritical, SeverityError} {
		b, err := Icon(IconPx, s)
		if err != nil {
			t.Fatalf("Icon(%v): %v", s, err)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("Icon(%v) did not encode a valid PNG: %v", s, err)
		}
		if r := img.Bounds(); r.Dx() != IconPx || r.Dy() != IconPx {
			t.Fatalf("Icon(%v) size = %dx%d, want %dx%d", s, r.Dx(), r.Dy(), IconPx, IconPx)
		}
	}
}

func TestIconRejectsNonPositiveSize(t *testing.T) {
	if _, err := Icon(0, SeverityOK); err == nil {
		t.Fatal("Icon(0, ...): want an error, got nil")
	}
	if _, err := Icon(-5, SeverityOK); err == nil {
		t.Fatal("Icon(-5, ...): want an error, got nil")
	}
}

// TestIconSeverityColorsDiffer is the actual load-bearing visual proof: a
// user glances at the tray, not at source code, so different severities
// must actually PAINT different dot colors, not just return distinct byte
// slices that happen to differ in some incidental way (e.g. PNG
// compression artifacts).
func TestIconSeverityColorsDiffer(t *testing.T) {
	pixelAt := func(t *testing.T, s Severity, x, y int) (r, g, b uint32) {
		t.Helper()
		data, err := Icon(IconPx, s)
		if err != nil {
			t.Fatalf("Icon(%v): %v", s, err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		rr, gg, bb, _ := img.At(x, y).RGBA()
		return rr >> 8, gg >> 8, bb >> 8
	}
	// Sample the dot's own center: bottom-right corner region, per Icon's
	// own placement (px - d/2 roughly).
	dx, dy := IconPx-IconPx/6, IconPx-IconPx/6

	okR, okG, okB := pixelAt(t, SeverityOK, dx, dy)
	critR, critG, critB := pixelAt(t, SeverityCritical, dx, dy)
	errR, errG, errB := pixelAt(t, SeverityError, dx, dy)

	if okR == critR && okG == critG && okB == critB {
		t.Fatalf("OK and Critical dots are the same color: (%d,%d,%d)", okR, okG, okB)
	}
	if critR == errR && critG == errG && critB == errB {
		t.Fatalf("Critical and Error dots are the same color: (%d,%d,%d) — a poll failure must look different from a high-quota warning, they need different user responses", critR, critG, critB)
	}
}

func TestIconsMapCoversEveryRealSeverity(t *testing.T) {
	icons := Icons(IconPx)
	for _, s := range []Severity{SeverityUnknown, SeverityOK, SeverityWarning, SeverityCritical, SeverityError} {
		frames, ok := icons[s]
		if !ok || len(frames) == 0 {
			t.Errorf("Icons()[%v] missing or empty", s)
		}
	}
}
