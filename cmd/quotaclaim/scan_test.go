// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	exhaustedMsg = "You've hit your weekly limit, resets Nov 12."
	generalMsg   = "The 5 h limit resets 14:00."
	plainMsg     = "Tests pass; PR 12 merged."
)

func assistantLine(text string) string {
	return `{"type":"assistant","message":{"role":"assistant","content":"` + text + `"}}` + "\n"
}

// transcripts writes root/proj/<name>.jsonl files and returns root.
func transcripts(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, "proj", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestScanTranscriptsCountsAndClassifies(t *testing.T) {
	root := transcripts(t, map[string]string{
		"a.jsonl": assistantLine(exhaustedMsg) + assistantLine(plainMsg) +
			`{"type":"user","message":{"role":"user","content":"hello"}}` + "\n",
		"b.jsonl": assistantLine(generalMsg),
		"c.txt":   assistantLine(exhaustedMsg), // not a transcript
	})
	// A transcript below a second directory level is not read either.
	deep := filepath.Join(root, "proj", "deep", "d.jsonl")
	if err := os.MkdirAll(filepath.Dir(deep), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deep, []byte(assistantLine(exhaustedMsg)), 0o600); err != nil {
		t.Fatal(err)
	}
	files, msgs, hits, err := scanTranscripts(root)
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || msgs != 3 || len(hits) != 2 {
		t.Fatalf("files %d messages %d hits %d, want 2 3 2", files, msgs, len(hits))
	}
	got := map[string]class{}
	for _, h := range hits {
		got[h.file] = h.c
		if h.hit == "" || h.context == "" {
			t.Errorf("empty hit or context: %+v", h)
		}
	}
	if got["a.jsonl"] != classExhausted || got["b.jsonl"] != classGeneral {
		t.Errorf("classes %v", got)
	}
}

func TestScanTranscriptsSkipsAnUnopenableFile(t *testing.T) {
	root := transcripts(t, map[string]string{"ok.jsonl": assistantLine(plainMsg)})
	// A dangling link matches the glob but cannot be opened.
	if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "proj", "dangling.jsonl")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	files, msgs, _, err := scanTranscripts(root)
	if err != nil || files != 1 || msgs != 1 {
		t.Errorf("files %d messages %d err %v, want 1 1 nil", files, msgs, err)
	}
}

func TestScanTranscriptsBadPattern(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("[ is not a pattern escape the same way on Windows")
	}
	if _, _, _, err := scanTranscripts("["); err == nil {
		t.Error("a malformed directory pattern was accepted")
	}
}

func TestOneLine(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"a  b\n\tc", 200, "a b c"},
		{"abcde", 5, "abcde"},
		{"abcdef", 5, "abcde…"},
		{"", 5, ""},
	}
	for _, c := range cases {
		if got := oneLine(c.in, c.n); got != c.want {
			t.Errorf("oneLine(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestPrintScan(t *testing.T) {
	hits := make([]scanHit, 5)
	for i := range hits {
		hits[i] = scanHit{file: "f.jsonl", hit: "h", context: "ctx", c: classGeneral}
	}
	hits[0].c = classExhausted
	run := func(hits []scanHit, sample int) string {
		var b bytes.Buffer
		if err := printScan(&b, 3, 9, hits, sample); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	const head = "files 3, assistant messages 9, matches "
	if got := run(nil, 20); got != head+"0\n" {
		t.Errorf("no hits: %q", got)
	}
	if got := run(hits, 0); got != head+"5\n" {
		t.Errorf("sample 0: %q", got)
	}
	if got := run(hits, -1); got != head+"5\n" {
		t.Errorf("sample -1: %q", got)
	}
	// Fewer hits than the sample: every one, in order.
	want := head + "5\n" +
		"[0] f.jsonl exhausted \"h\" :: ctx\n" +
		"[1] f.jsonl general \"h\" :: ctx\n" +
		"[2] f.jsonl general \"h\" :: ctx\n" +
		"[3] f.jsonl general \"h\" :: ctx\n" +
		"[4] f.jsonl general \"h\" :: ctx\n"
	if got := run(hits, 20); got != want {
		t.Errorf("all hits:\n%s\nwant:\n%s", got, want)
	}
	// More hits than the sample: evenly spaced, never more than the sample.
	want = head + "5\n" +
		"[0] f.jsonl exhausted \"h\" :: ctx\n" +
		"[2] f.jsonl general \"h\" :: ctx\n"
	if got := run(hits, 2); got != want {
		t.Errorf("sample 2:\n%s\nwant:\n%s", got, want)
	}
	want = head + "5\n" + "[0] f.jsonl exhausted \"h\" :: ctx\n"
	if got := run(hits, 1); got != want {
		t.Errorf("sample 1:\n%s\nwant:\n%s", got, want)
	}
}

func scan(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := RootCmd(strings.NewReader(""))
	var o, e bytes.Buffer
	cmd.SetArgs(append([]string{"scan"}, args...))
	cmd.SetOut(&o)
	cmd.SetErr(&e)
	err := cmd.Execute()
	return o.String(), err
}

func TestScanCommand(t *testing.T) {
	root := transcripts(t, map[string]string{"a.jsonl": assistantLine(exhaustedMsg) + assistantLine(generalMsg)})
	out, err := scan(t, "--dir", root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "files 1, assistant messages 2, matches 2\n") || strings.Count(out, "\n") != 3 {
		t.Errorf("out %q", out)
	}
	out, err = scan(t, "--dir", root, "--sample", "0")
	if err != nil || out != "files 1, assistant messages 2, matches 2\n" {
		t.Errorf("sample 0: %q %v", out, err)
	}
	if _, err := scan(t, "--dir", root, "extra"); err == nil {
		t.Error("an argument was accepted")
	}
	if runtime.GOOS != "windows" {
		if _, err := scan(t, "--dir", "["); err == nil {
			t.Error("a bad pattern was accepted")
		}
	}
}

func homeEnv(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("home", home)
}

func TestScanDefaultsToTheClaudeProjectsDir(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, ".claude", "projects", "proj", "a.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(assistantLine(generalMsg)), 0o600); err != nil {
		t.Fatal(err)
	}
	homeEnv(t, home)
	out, err := scan(t)
	if err != nil || !strings.HasPrefix(out, "files 1, assistant messages 1, matches 1\n") {
		t.Errorf("out %q err %v", out, err)
	}
}

func TestScanWithoutAHomeDirFails(t *testing.T) {
	if runtime.GOOS == "plan9" {
		t.Skip("plan9 has a fixed home")
	}
	homeEnv(t, "")
	if out, err := scan(t); err == nil {
		t.Errorf("no home dir, yet scanned: %q", out)
	}
}
