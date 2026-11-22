// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package stamp

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCachePathIsStablePerKey(t *testing.T) {
	a := CachePath("d", "claude|acct|overlay")
	if a != CachePath("d", "claude|acct|overlay") {
		t.Error("same key, different path")
	}
	if a == CachePath("d", "claude|acct|other") {
		t.Error("different keys share a path")
	}
	if filepath.Dir(a) != "d" || !strings.HasPrefix(filepath.Base(a), "cache-") || !strings.HasSuffix(a, ".json") {
		t.Errorf("path %q", a)
	}
}

func TestHasReading(t *testing.T) {
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		e    CacheEntry
		want bool
	}{
		{"empty", CacheEntry{}, false},
		{"available", CacheEntry{Reading: Stamp{At: at, Verdict: Available}}, true},
		{"exhausted", CacheEntry{Reading: Stamp{At: at, Verdict: Exhausted}}, true},
		{"unknown is no reading", CacheEntry{Reading: Stamp{At: at, Verdict: Unknown}}, false},
		{"good verdict without a time", CacheEntry{Reading: Stamp{Verdict: Available}}, false},
	}
	for _, c := range cases {
		if got := c.e.HasReading(); got != c.want {
			t.Errorf("%s: HasReading = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCacheRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d", "cache.json")
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	nb := at.Add(time.Minute)
	in := CacheEntry{Reading: Stamp{At: at, Verdict: Available, Stale: "rate limited"}, NotBefore: nb}
	if err := WriteCache(p, in); err != nil {
		t.Fatal(err)
	}
	got := ReadCache(p)
	if got.Version != Version || !got.Reading.At.Equal(at) || got.Reading.Stale != "rate limited" || !got.NotBefore.Equal(nb) {
		t.Fatalf("got %+v", got)
	}
	if fi, _ := os.Stat(p); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	// A zero NotBefore is omitted, not written as year 1.
	if err := WriteCache(p, CacheEntry{}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if strings.Contains(string(data), "not_before") {
		t.Errorf("zero not_before written: %s", data)
	}
}

func TestReadCacheMissIsEmpty(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"missing": filepath.Join(dir, "none.json"), "unreadable": dir, "corrupt": bad} {
		if got := ReadCache(p); got.HasReading() || got.Version != 0 || !got.NotBefore.IsZero() {
			t.Errorf("%s: %+v, want the empty entry", name, got)
		}
	}
}

func TestWriteFailures(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	nan := math.NaN()
	// A value json cannot encode.
	if err := Write(filepath.Join(dir, "a.json"), Stamp{Windows: []Window{{Used: nan}}}); err == nil {
		t.Error("Write: NaN encoded")
	}
	if err := WriteCache(filepath.Join(dir, "a.json"), CacheEntry{Reading: Stamp{Windows: []Window{{Used: nan}}}}); err == nil {
		t.Error("WriteCache: NaN encoded")
	}
	// The directory cannot be created: its parent is a file.
	if err := Write(filepath.Join(file, "x", "a.json"), Stamp{}); err == nil {
		t.Error("Write: directory under a file")
	}
	if err := WriteCache(filepath.Join(file, "x", "a.json"), CacheEntry{}); err == nil {
		t.Error("WriteCache: directory under a file")
	}
	// The destination is a directory: the rename fails and no temp file stays.
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Write(target, Stamp{}); err == nil {
		t.Error("Write: rename over a directory")
	}
	if err := WriteCache(target, CacheEntry{}); err == nil {
		t.Error("WriteCache: rename over a directory")
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".*-*"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// failingFile is a temp file whose Write or Close fails.
type failingFile struct {
	*os.File
	failWrite, failClose bool
}

var errInjected = errors.New("injected")

func (f failingFile) Write(b []byte) (int, error) {
	if f.failWrite {
		return 0, errInjected
	}
	return f.File.Write(b)
}

func (f failingFile) Close() error {
	err := f.File.Close()
	if f.failClose {
		return errInjected
	}
	return err
}

func TestAtomicWriteInjectedFailures(t *testing.T) {
	orig := createTemp
	t.Cleanup(func() { createTemp = orig })
	dir := t.TempDir()
	target := filepath.Join(dir, "out")

	createTemp = func(string, string) (tempFile, error) { return nil, errInjected }
	if err := atomicWrite(target, ".x-*", []byte("v")); !errors.Is(err, errInjected) {
		t.Errorf("create: %v", err)
	}
	for name, ff := range map[string]failingFile{"write": {failWrite: true}, "close": {failClose: true}} {
		createTemp = func(d, p string) (tempFile, error) {
			f, err := os.CreateTemp(d, p)
			if err != nil {
				return nil, err
			}
			ff.File = f
			return ff, nil
		}
		if err := atomicWrite(target, ".x-*", []byte("v")); !errors.Is(err, errInjected) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := os.Stat(target); err == nil {
			t.Errorf("%s: the destination was written", name)
		}
		left, _ := filepath.Glob(filepath.Join(dir, ".x-*"))
		if len(left) != 0 {
			t.Errorf("%s: temp files left behind: %v", name, left)
		}
	}
}

func TestDefaultCreateTempReturnsAFileOrAnError(t *testing.T) {
	f, err := createTemp(t.TempDir(), ".x-*")
	if err != nil || f == nil {
		t.Fatalf("%v %v", f, err)
	}
	_ = f.Close()
	if g, err := createTemp(filepath.Join(t.TempDir(), "missing"), ".x-*"); err == nil || g != nil {
		t.Errorf("a missing directory gave %v, %v", g, err)
	}
}

func TestLockExclusiveAndReleased(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d", "cache.json")
	release, err := Lock(p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p + ".lock")
	if err != nil || strings.TrimSpace(string(data)) == "" {
		t.Fatalf("lock file: %q %v", data, err)
	}
	if _, err := Lock(p, 0); !errors.Is(err, ErrLockTimeout) {
		t.Errorf("a second holder got %v, want ErrLockTimeout", err)
	}
	release()
	if _, err := os.Stat(p + ".lock"); !os.IsNotExist(err) {
		t.Errorf("lock not removed: %v", err)
	}
	release2, err := Lock(p, 0)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release2()
}

func TestLockWaitsForItsHolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	release, err := Lock(p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		release()
	}()
	start := time.Now()
	release2, err := Lock(p, 5*time.Second)
	if err != nil {
		t.Fatalf("waiter: %v", err)
	}
	defer release2()
	if time.Since(start) < 50*time.Millisecond {
		t.Errorf("the waiter did not wait: %v", time.Since(start))
	}
}

func TestLockTimesOutAfterItsWait(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	release, err := Lock(p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := time.Now()
	if _, err := Lock(p, 100*time.Millisecond); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Errorf("gave up after %v, before its wait", d)
	}
}

func TestLockBreaksAStaleLockOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	lock := p + ".lock"
	if err := os.WriteFile(lock, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Just under lockStale: still a live holder's.
	young := time.Now().Add(-lockStale + time.Minute)
	if err := os.Chtimes(lock, young, young); err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(p, 0); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("a young lock was broken: %v", err)
	}
	// Past it: a crashed holder's, broken, and the caller gets the lock at once.
	old := time.Now().Add(-lockStale - time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	release, err := Lock(p, 0)
	if err != nil {
		t.Fatalf("a stale lock was not broken: %v", err)
	}
	release()
}

func TestLockDirectoryFailure(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(filepath.Join(file, "x", "cache.json"), 0); err == nil {
		t.Error("a lock under a file")
	}
}

// readOnlyDir returns a directory in which no file can be created, or skips.
func readOnlyDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("cannot make a directory refuse a file creation here")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}

// Regression, 581fd9f: on Windows a lock file just removed by its holder is
// delete pending, and creating it fails with access denied, not "exists". That
// is contention (retry until the wait ends), not an error to return at once.
// Elsewhere access denied stays a real error.
func TestLockAccessDeniedIsContentionOnWindowsOnly(t *testing.T) {
	p := filepath.Join(readOnlyDir(t), "cache.json")
	orig := goos
	t.Cleanup(func() { goos = orig })

	goos = "windows"
	if _, err := Lock(p, 60*time.Millisecond); !errors.Is(err, ErrLockTimeout) {
		t.Errorf("windows: got %v, want ErrLockTimeout", err)
	}
	goos = "linux"
	_, err := Lock(p, 60*time.Millisecond)
	if err == nil || errors.Is(err, ErrLockTimeout) || !errors.Is(err, os.ErrPermission) {
		t.Errorf("linux: got %v, want the permission error", err)
	}
}

func TestStaleSurvivesJSON(t *testing.T) {
	data, err := json.Marshal(Stamp{Stale: "rate limited"})
	if err != nil || !strings.Contains(string(data), `"stale":"rate limited"`) {
		t.Fatalf("%s %v", data, err)
	}
	data, _ = json.Marshal(Stamp{})
	if strings.Contains(string(data), "stale") {
		t.Errorf("empty stale written: %s", data)
	}
}
