// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package stamp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		// 6e92724: a window listed without a value takes no part in the verdict.
		{w(Available, NotAvailable), Available},
		{w(NotAvailable, Available), Available},
		{w(NotAvailable, Unknown), Unknown},
		{w(NotAvailable, Exhausted), Exhausted},
		// Every window n/a: nothing was measured.
		{w(NotAvailable), Unknown},
		{w(NotAvailable, NotAvailable), Unknown},
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

// 38d7840: a window carries its plan, written only when there is one.
func TestWindowPlanJSON(t *testing.T) {
	data, err := json.Marshal(Window{Label: "five_hour", Plan: "Max 20x"})
	if err != nil || !strings.Contains(string(data), `"plan":"Max 20x"`) {
		t.Fatalf("%s %v", data, err)
	}
	var w Window
	if err := json.Unmarshal(data, &w); err != nil || w.Plan != "Max 20x" {
		t.Fatalf("%+v %v", w, err)
	}
	data, _ = json.Marshal(Window{Label: "five_hour"})
	if strings.Contains(string(data), "plan") {
		t.Errorf("empty plan written: %s", data)
	}
}

func TestDefaultPath(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("APPDATA", cfg)
	t.Setenv("HOME", cfg)
	got, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "last-status.json" || filepath.Base(filepath.Dir(got)) != "aiquota" {
		t.Errorf("path %q", got)
	}
}

func TestDefaultPathNoConfigDir(t *testing.T) {
	if runtime.GOOS == "plan9" {
		t.Skip("plan9 has a fixed home")
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "")
	t.Setenv("HOME", "")
	if p, err := DefaultPath(); err == nil {
		t.Errorf("got %q without a config dir", p)
	}
}

func TestReadFailures(t *testing.T) {
	dir := t.TempDir()
	if _, err := Read(dir); err == nil || errors.Is(err, ErrNoStamp) {
		t.Errorf("a directory: %v", err)
	} else if strings.Contains(err.Error(), "stamp:") {
		// the read error itself, not the JSON error of decoding no bytes
		t.Errorf("a directory read must fail as a read: %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(bad); err == nil || errors.Is(err, ErrNoStamp) || !strings.Contains(err.Error(), bad) {
		t.Errorf("corrupt file: %v", err)
	}
}
