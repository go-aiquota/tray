// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"testing"
	"time"
)

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

func TestSeriesWindowDurationKnownKeys(t *testing.T) {
	for key, want := range map[string]time.Duration{"session": 5 * time.Hour, "weekly": 7 * 24 * time.Hour} {
		got, ok := SeriesWindowDuration(key)
		if !ok {
			t.Errorf("SeriesWindowDuration(%q): ok = false, want true", key)
		}
		if got != want {
			t.Errorf("SeriesWindowDuration(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestSeriesWindowDurationUnknownKey(t *testing.T) {
	if _, ok := SeriesWindowDuration("not-a-real-series"); ok {
		t.Fatal("SeriesWindowDuration(unknown key): ok = true, want false")
	}
}

// TestOverPaceColorDiffersFromSeriesColors is the load-bearing proof: an
// over-pace stretch of curve must not blend into either series' own
// normal color, or the recoloring this exists for would be invisible.
func TestOverPaceColorDiffersFromSeriesColors(t *testing.T) {
	over := OverPaceColor()
	for _, key := range SeriesOrder() {
		if c, ok := SeriesColor(key); ok && c == over {
			t.Errorf("OverPaceColor() matches SeriesColor(%q); an over-pace stretch would be indistinguishable from normal", key)
		}
	}
}
