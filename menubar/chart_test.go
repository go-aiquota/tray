// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import "testing"

func TestSeriesOrderIsStableAndComplete(t *testing.T) {
	want := []string{"session", "weekly"}
	got := SeriesOrder()
	if len(got) != len(want) {
		t.Fatalf("SeriesOrder() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SeriesOrder() = %v, want %v", got, want)
		}
	}
}

// TestSeriesOrderReturnsACopy proves a caller mutating the returned
// slice (a legend building its own row order, say) can't corrupt the
// package's own fixed order for the next caller.
func TestSeriesOrderReturnsACopy(t *testing.T) {
	got := SeriesOrder()
	got[0] = "corrupted"
	if again := SeriesOrder(); again[0] == "corrupted" {
		t.Fatal("SeriesOrder() returned a slice sharing backing storage across calls")
	}
}

func TestSeriesLabelKnownKeys(t *testing.T) {
	for key, want := range map[string]string{"session": "Session", "weekly": "Weekly"} {
		got, ok := SeriesLabel(key)
		if !ok {
			t.Errorf("SeriesLabel(%q): ok = false, want true", key)
		}
		if got != want {
			t.Errorf("SeriesLabel(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestSeriesLabelUnknownKey(t *testing.T) {
	if _, ok := SeriesLabel("not-a-real-series"); ok {
		t.Fatal("SeriesLabel(unknown key): ok = true, want false")
	}
}

func TestSeriesColorKnownKeys(t *testing.T) {
	for _, key := range SeriesOrder() {
		if _, ok := SeriesColor(key); !ok {
			t.Errorf("SeriesColor(%q): ok = false, want true", key)
		}
	}
}

func TestSeriesColorUnknownKey(t *testing.T) {
	if _, ok := SeriesColor("not-a-real-series"); ok {
		t.Fatal("SeriesColor(unknown key): ok = true, want false")
	}
}

// TestSeriesColorsAreDistinct is the actual load-bearing proof: session
// and weekly must draw in different colors, or two overlapping curves
// (or the legend swatches describing them) would be indistinguishable.
func TestSeriesColorsAreDistinct(t *testing.T) {
	session, _ := SeriesColor("session")
	weekly, _ := SeriesColor("weekly")
	if session == weekly {
		t.Fatal("session and weekly must not share a color")
	}
}
