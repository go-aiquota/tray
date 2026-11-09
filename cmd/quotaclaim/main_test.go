// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aiquota/tray/stamp"
)

var t0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func hook(t *testing.T, msg, path string, extra ...string) (code int, out string) {
	t.Helper()
	now = func() time.Time { return t0 }
	in, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "last_assistant_message": msg})
	var o, e bytes.Buffer
	code = run(append([]string{"--stamp", path}, extra...), bytes.NewReader(in), &o, &e)
	return code, o.String()
}

func put(t *testing.T, v stamp.Verdict, age time.Duration) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "last-status.json")
	if err := stamp.Write(p, stamp.Stamp{At: t0.Add(-age), Verdict: v}); err != nil {
		t.Fatal(err)
	}
	return p
}

func blocked(out string) bool {
	var d map[string]string
	return json.Unmarshal([]byte(out), &d) == nil && d["decision"] == "block" && strings.Contains(d["reason"], "aiquota status")
}

// Positive control: the 2026-11-09 incident, with no stamp at all.
func TestClaimWithNoStampIsBlocked(t *testing.T) {
	for _, msg := range []string{
		"You've hit your weekly limit · resets Nov 12, so I am blocked until Nov 12.",
		"Je suis bloqué jusqu'au 12 novembre : limite hebdomadaire atteinte.",
		"The 5 h limit resets 14:00.",
	} {
		code, out := hook(t, msg, filepath.Join(t.TempDir(), "none.json"))
		if code != 0 || !blocked(out) {
			t.Errorf("%q: code %d out %q, want a block", msg, code, out)
		}
	}
}

func TestFreshMatchingStampAllows(t *testing.T) {
	p := put(t, stamp.Exhausted, 2*time.Minute)
	if code, out := hook(t, "You've hit your weekly limit, resets Nov 12.", p); code != 0 || out != "" {
		t.Errorf("code %d out %q, want allow", code, out)
	}
	p = put(t, stamp.Available, time.Minute)
	if _, out := hook(t, "Weekly limit: 40% used, plenty left.", p); out != "" {
		t.Errorf("fresh available stamp must back a non-exhausted statement, got %q", out)
	}
}

func TestStaleStampIsBlocked(t *testing.T) {
	p := put(t, stamp.Exhausted, 3*time.Hour)
	if _, out := hook(t, "weekly limit hit", p); !blocked(out) {
		t.Errorf("stale stamp allowed: %q", out)
	}
	// --max-age widens the window.
	if _, out := hook(t, "weekly limit hit", p, "--max-age", "4h"); out != "" {
		t.Errorf("max-age 4h should allow: %q", out)
	}
}

func TestContradictedClaimIsBlocked(t *testing.T) {
	p := put(t, stamp.Available, time.Minute)
	if _, out := hook(t, "You've hit your weekly limit.", p); !blocked(out) {
		t.Errorf("exhausted claim against an available stamp allowed: %q", out)
	}
}

func TestUnknownStampBacksNothing(t *testing.T) {
	p := put(t, stamp.Unknown, time.Minute)
	if _, out := hook(t, "weekly limit resets Nov 12", p); out == "" {
		t.Error("unknown stamp allowed a claim")
	}
}

func TestUnrelatedTextAllowed(t *testing.T) {
	none := filepath.Join(t.TempDir(), "none.json")
	for _, msg := range []string{
		"Tests pass; PR 12 merged.",
		"The aiquota CLI is in go-aiquota/tray.",
		"The cache resets to zero on Close.",
		"Pas de limite de taille ici.",
		"",
	} {
		if code, out := hook(t, msg, none); code != 0 || out != "" {
			t.Errorf("%q: code %d out %q, want allow", msg, code, out)
		}
	}
}

func TestTranscriptFallback(t *testing.T) {
	dir := t.TempDir()
	tp := filepath.Join(dir, "t.jsonl")
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"weekly limit reached, resets Nov 12"}]}}` + "\n"
	if err := os.WriteFile(tp, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"transcript_path": tp})
	now = func() time.Time { return t0 }
	var o, e bytes.Buffer
	run([]string{"--stamp", filepath.Join(dir, "none.json")}, bytes.NewReader(in), &o, &e)
	if !blocked(o.String()) {
		t.Errorf("transcript claim not blocked: %q", o.String())
	}
}

func TestBadInputDoesNotBlock(t *testing.T) {
	var o, e bytes.Buffer
	if code := run(nil, strings.NewReader("not json"), &o, &e); code != 1 || o.Len() != 0 {
		t.Errorf("code %d out %q: bad input must be a non-blocking error", code, o.String())
	}
}
