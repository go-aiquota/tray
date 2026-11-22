// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

// The 2026-11-09 20:42 false positive: a limit placed in the past, after a
// fresh AVAILABLE reading, is history, not a contradiction. It still needs
// that reading (no stamp → blocked), and a present marker keeps it a claim.
func TestPastLimitNeedsAFreshReadingNotAnExhaustedOne(t *testing.T) {
	past := "They stopped earlier when they hit the 5-hour limit."
	if _, out := hook(t, past, put(t, stamp.Available, time.Minute)); out != "" {
		t.Errorf("past limit against a fresh available stamp blocked: %q", out)
	}
	if _, out := hook(t, past, filepath.Join(t.TempDir(), "none.json")); !blocked(out) {
		t.Errorf("past limit with no stamp allowed: %q", out)
	}
	for _, msg := range []string{
		"They hit the 5-hour limit earlier and are still blocked.",
		"I hit the weekly limit earlier, so I can't continue until Nov 12.",
		"Ils avaient atteint la limite de 5 h et sont encore bloqués.",
	} {
		if _, out := hook(t, msg, put(t, stamp.Available, time.Minute)); !blocked(out) {
			t.Errorf("%q: present-tense claim allowed against an available stamp: %q", msg, out)
		}
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

func TestUnknownStampAllowsOnlyAnUnmeasuredStatement(t *testing.T) {
	p := put(t, stamp.Unknown, time.Minute)
	if _, out := hook(t, "The weekly limit state is unknown, I could not measure it.", p); out != "" {
		t.Errorf("a statement that says unknown was blocked: %q", out)
	}
	if _, out := hook(t, "Blocked until Nov 12 by the weekly limit.", p); !blocked(out) {
		t.Errorf("an assertion on an UNKNOWN stamp was allowed: %q", out)
	}
	none := filepath.Join(t.TempDir(), "none.json")
	if _, out := hook(t, "Weekly limit: not measured yet.", none); out != "" {
		t.Errorf("hedged statement with no stamp was blocked: %q", out)
	}
}

func TestStopHookActiveDoesNotLoop(t *testing.T) {
	now = func() time.Time { return t0 }
	in, _ := json.Marshal(map[string]any{"last_assistant_message": "weekly limit hit, blocked until Nov 12", "stop_hook_active": true})
	var o, e bytes.Buffer
	run([]string{"--stamp", filepath.Join(t.TempDir(), "none.json")}, bytes.NewReader(in), &o, &e)
	if o.Len() != 0 {
		t.Errorf("second pass must allow, got %q", o.String())
	}
}

func TestUnreadableStampIsBlockedWithItsError(t *testing.T) {
	// A directory where the stamp should be: not "no measurement", unreadable.
	_, out := hook(t, "weekly limit hit", t.TempDir())
	var d map[string]string
	if err := json.Unmarshal([]byte(out), &d); err != nil || d["decision"] != "block" || !strings.Contains(d["reason"], "the stamp is unreadable (") {
		t.Errorf("out %q", out)
	}
	// An unreadable stamp still lets a statement that says "unknown" through.
	if _, out := hook(t, "The weekly limit state is unknown.", t.TempDir()); out != "" {
		t.Errorf("hedged statement blocked: %q", out)
	}
}

func TestBlockReasonsNameTheirCause(t *testing.T) {
	reason := func(msg, p string) string {
		t.Helper()
		_, out := hook(t, msg, p)
		var d map[string]string
		if err := json.Unmarshal([]byte(out), &d); err != nil {
			t.Fatalf("%q: %v", out, err)
		}
		return d["reason"]
	}
	if r := reason("weekly limit hit", filepath.Join(t.TempDir(), "none.json")); !strings.Contains(r, "with no measurement on record") {
		t.Error(r)
	}
	if r := reason("weekly limit hit", put(t, stamp.Exhausted, 3*time.Hour)); !strings.Contains(r, "the last measurement is 3h0m0s old (limit 10m0s)") {
		t.Error(r)
	}
	if r := reason("weekly limit hit", put(t, stamp.Unknown, time.Minute)); !strings.Contains(r, "verdict UNKNOWN") {
		t.Error(r)
	}
	if r := reason("You've hit your weekly limit.", put(t, stamp.Available, time.Minute)); !strings.Contains(r, "says available") {
		t.Error(r)
	}
}

// With no --stamp the default path under the user config dir is read; when no
// config dir can be resolved the claim is refused, not waved through.
func TestDefaultStampPath(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("APPDATA", cfg)
	t.Setenv("HOME", cfg)
	dp, err := stamp.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := stamp.Write(dp, stamp.Stamp{At: t0.Add(-time.Minute), Verdict: stamp.Exhausted}); err != nil {
		t.Fatal(err)
	}
	if got := path(""); got != dp {
		t.Errorf("path(\"\") = %q, want %q", got, dp)
	}
	if got := path("x.json"); got != "x.json" {
		t.Errorf("path(x.json) = %q", got)
	}
	now = func() time.Time { return t0 }
	in, _ := json.Marshal(map[string]any{"last_assistant_message": "weekly limit hit"})
	var o, e bytes.Buffer
	if code := run(nil, bytes.NewReader(in), &o, &e); code != 0 || o.Len() != 0 {
		t.Errorf("default stamp not honoured: code %d out %q", code, o.String())
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "")
	t.Setenv("HOME", "")
	if runtime.GOOS == "plan9" {
		return
	}
	if got := path(""); got != "" {
		t.Errorf("path(\"\") = %q with no config dir", got)
	}
	o.Reset()
	if code := run(nil, bytes.NewReader(in), &o, &e); code != 0 || !blocked(o.String()) {
		t.Errorf("claim waved through with no stamp path: code %d out %q", code, o.String())
	}
}

func TestAssistantTexts(t *testing.T) {
	const lines = `not json
{"type":"user","message":{"role":"user","content":"a user message"}}
{"type":"assistant","message":{"role":"assistant","content":"plain string"}}
{"message":{"role":"assistant","content":[{"type":"text","text":"one"},{"type":"tool_use"},{"type":"text","text":"two"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use"}]}}
{"type":"assistant","message":{"role":"assistant","content":42}}
{"type":"assistant","message":{"role":"assistant"}}
`
	var got []string
	assistantTexts(strings.NewReader(lines), func(s string) { got = append(got, s) })
	want := []string{"plain string", "one\ntwo\n"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLastAssistantText(t *testing.T) {
	r := strings.NewReader(assistantLine("first") + assistantLine("second"))
	if got := lastAssistantText(r); got != "second" {
		t.Errorf("got %q", got)
	}
	if got := lastAssistantText(strings.NewReader("")); got != "" {
		t.Errorf("got %q", got)
	}
}

// A transcript path that cannot be opened leaves the message empty: nothing is
// asserted, so the stop is allowed.
func TestUnopenableTranscriptAllows(t *testing.T) {
	in, _ := json.Marshal(map[string]any{"transcript_path": filepath.Join(t.TempDir(), "gone.jsonl")})
	var o, e bytes.Buffer
	if code := run([]string{"--stamp", filepath.Join(t.TempDir(), "none.json")}, bytes.NewReader(in), &o, &e); code != 0 || o.Len() != 0 {
		t.Errorf("code %d out %q", code, o.String())
	}
}

func TestVersionFlag(t *testing.T) {
	var o, e bytes.Buffer
	if code := run([]string{"--version"}, strings.NewReader(""), &o, &e); code != 0 || !strings.HasPrefix(o.String(), "quotaclaim ") || strings.TrimSpace(strings.TrimPrefix(o.String(), "quotaclaim ")) == "" {
		t.Errorf("code %d out %q", code, o.String())
	}
}

func TestMainExitsWithRunsCode(t *testing.T) {
	oldExit, oldArgs, oldIn := osExit, os.Args, os.Stdin
	t.Cleanup(func() { osExit, os.Args, os.Stdin = oldExit, oldArgs, oldIn })
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	os.Stdin = in
	os.Args = []string{"quotaclaim", "--version"}
	code := -1
	osExit = func(c int) { code = c }
	main()
	if code != 0 {
		t.Errorf("exit code %d", code)
	}
	// An empty stdin is not hook JSON: a non-blocking error, exit 1.
	os.Args = []string{"quotaclaim"}
	main()
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
}
