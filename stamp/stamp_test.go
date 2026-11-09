// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package stamp

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestOverall(t *testing.T) {
	w := func(v ...Verdict) []Window {
		var out []Window
		for _, x := range v {
			out = append(out, Window{Verdict: x})
		}
		return out
	}
	cases := []struct {
		in   []Window
		want Verdict
	}{
		{nil, Unknown},
		{w(Available, Available), Available},
		{w(Available, Unknown), Unknown},
		{w(Unknown, Exhausted), Exhausted},
		{w(Available, Exhausted), Exhausted},
	}
	for _, c := range cases {
		if got := Overall(c.in); got != c.want {
			t.Errorf("Overall(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestRoundTripAndMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d", "last-status.json")
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	if _, err := Read(p); !errors.Is(err, ErrNoStamp) {
		t.Fatalf("missing file: %v", err)
	}
	if err := Write(p, Stamp{At: at, Verdict: Exhausted, Windows: []Window{{Label: "weekly_all", Verdict: Exhausted}}}); err != nil {
		t.Fatal(err)
	}
	got, err := Read(p)
	if err != nil || got.Verdict != Exhausted || !got.At.Equal(at) || got.Version != Version {
		t.Fatalf("got %+v err %v", got, err)
	}
	if fi, _ := os.Stat(p); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if got.Age(at.Add(-time.Hour)) != 0 || got.Age(at.Add(time.Minute)) != time.Minute {
		t.Error("Age")
	}
}
