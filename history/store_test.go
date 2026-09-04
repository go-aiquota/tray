// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package history

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "history.json")}
}

func TestAppendThenRecentRoundTrips(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	if err := s.Append("acct-1", "session", Point{AtUnix: now.Unix(), Used: 10, Limit: 100}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.Append("acct-1", "session", Point{AtUnix: now.Add(5 * time.Minute).Unix(), Used: 20, Limit: 100}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.Recent("acct-1", "session", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Recent() = %d points, want 2", len(got))
	}
	if got[0].Used != 10 || got[1].Used != 20 {
		t.Fatalf("Recent() = %+v, want ascending [10, 20]", got)
	}
}

func TestAppendKeepsSeriesSeparate(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	if err := s.Append("acct-1", "session", Point{AtUnix: now.Unix(), Used: 1, Limit: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("acct-1", "weekly", Point{AtUnix: now.Unix(), Used: 2, Limit: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("acct-2", "session", Point{AtUnix: now.Unix(), Used: 3, Limit: 100}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		account, window string
		want            float64
	}{
		{"acct-1", "session", 1},
		{"acct-1", "weekly", 2},
		{"acct-2", "session", 3},
	} {
		got, err := s.Recent(c.account, c.window, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Used != c.want {
			t.Fatalf("Recent(%q, %q) = %+v, want one point with Used=%v", c.account, c.window, got, c.want)
		}
	}
}

func TestAppendPrunesOlderThanRetention(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	old := Point{AtUnix: now.Add(-Retention - time.Hour).Unix(), Used: 1, Limit: 100}
	if err := s.Append("acct-1", "session", old); err != nil {
		t.Fatal(err)
	}
	fresh := Point{AtUnix: now.Unix(), Used: 2, Limit: 100}
	if err := s.Append("acct-1", "session", fresh); err != nil {
		t.Fatal(err)
	}

	got, err := s.Recent("acct-1", "session", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Used != 2 {
		t.Fatalf("Recent() = %+v, want only the fresh point (the old one should have been pruned)", got)
	}
}

func TestRecentFiltersBySince(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	if err := s.Append("acct-1", "session", Point{AtUnix: now.Add(-time.Hour).Unix(), Used: 1, Limit: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("acct-1", "session", Point{AtUnix: now.Unix(), Used: 2, Limit: 100}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Recent("acct-1", "session", now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Used != 2 {
		t.Fatalf("Recent(since=now-1m) = %+v, want only the point at now", got)
	}
}

func TestRecentOnAnEmptyStoreReturnsNilNoError(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Recent("nobody", "session", time.Time{})
	if err != nil {
		t.Fatalf("Recent on a store whose file doesn't exist yet: %v, want nil error", err)
	}
	if got != nil {
		t.Fatalf("Recent() = %v, want nil", got)
	}
}

func TestAppendPersistsAcrossStoreInstances(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	now := time.Now()

	if err := (&Store{Path: path}).Append("acct-1", "session", Point{AtUnix: now.Unix(), Used: 5, Limit: 100}); err != nil {
		t.Fatal(err)
	}
	got, err := (&Store{Path: path}).Recent("acct-1", "session", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Used != 5 {
		t.Fatalf("a fresh Store reading the same path got %+v, want the point the first Store wrote", got)
	}
}

// TestWriteIsAtomicOnFailure proves a failed write never corrupts or
// truncates the file that was already there: writeAll always goes
// through a temp file + os.Rename, so a write that fails partway (here,
// because the directory has been made unwritable) must leave the
// PREVIOUS good file untouched rather than a half-written one.
//
// Skipped on Windows: a directory's write permission bit does not block
// creating a file in it the way POSIX's does, so this specific fault
// injection doesn't reach the code path being tested there.
func TestWriteIsAtomicOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits don't block file creation on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	s := &Store{Path: path}
	now := time.Now()

	if err := s.Append("acct-1", "session", Point{AtUnix: now.Unix(), Used: 1, Limit: 100}); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := s.Append("acct-1", "session", Point{AtUnix: now.Add(time.Minute).Unix(), Used: 2, Limit: 100}); err == nil {
		t.Fatal("Append into a read-only directory: want an error, got nil")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("the previous file changed after a failed write:\nbefore: %s\nafter:  %s", before, after)
	}
}
