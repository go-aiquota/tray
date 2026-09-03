// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package capture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWriteEntriesRoundTrips(t *testing.T) {
	dir := t.TempDir()
	entries := []Entry{
		{At: time.Now(), Method: "GET", URL: "https://claude.ai/api/usage", Status: 200, Body: `{"used":1}`},
	}
	path, err := WriteEntries(dir, entries)
	if err != nil {
		t.Fatalf("WriteEntries: %v", err)
	}
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("path = %q, want it under %q", path, dir)
	}
	if !strings.HasSuffix(path, ".json") {
		t.Fatalf("path = %q, want a .json file", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var got []Entry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("the written file is not valid JSON: %v", err)
	}
	if len(got) != 1 || got[0].URL != "https://claude.ai/api/usage" {
		t.Fatalf("round-tripped entries = %+v, want the original", got)
	}
}

func TestWriteEntriesCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "captures")
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("test setup: directory already exists")
	}
	if _, err := WriteEntries(dir, []Entry{{URL: "x"}}); err != nil {
		t.Fatalf("WriteEntries: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("WriteEntries did not create %s", dir)
	}
}

func TestWriteEntriesIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX group/other permission bits to assert on; os.WriteFile's mode is not observable the same way")
	}
	dir := t.TempDir()
	path, err := WriteEntries(dir, []Entry{{URL: "x"}})
	if err != nil {
		t.Fatalf("WriteEntries: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("capture file mode = %v, want no group/other permissions (it may hold redacted but real account traffic)", perm)
	}
}

func TestWriteEntriesTwoCallsDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	p1, err := WriteEntries(dir, []Entry{{URL: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // the filename's resolution is milliseconds
	p2, err := WriteEntries(dir, []Entry{{URL: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Fatalf("two WriteEntries calls produced the same path %q", p1)
	}
}
