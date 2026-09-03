// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package app

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestCredentialCacheFetchesOnceUntilLock(t *testing.T) {
	var calls int32
	c := NewCredentialCache(func(id string) (map[string]string, error) {
		atomic.AddInt32(&calls, 1)
		return map[string]string{"cookie": "value-for-" + id}, nil
	})

	for range 3 {
		v, err := c.Get("acct-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if v["cookie"] != "value-for-acct-1" {
			t.Fatalf("Get = %v, want the fetched credential", v)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("underlying fetch called %d times, want exactly 1 (cached after the first)", got)
	}

	c.Lock()
	if _, err := c.Get("acct-1"); err != nil {
		t.Fatalf("Get after Lock: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("underlying fetch called %d times after Lock+Get, want 2 (Lock must force a re-fetch)", got)
	}
}

func TestCredentialCacheCachesPerAccount(t *testing.T) {
	seen := map[string]int{}
	c := NewCredentialCache(func(id string) (map[string]string, error) {
		seen[id]++
		return map[string]string{"id": id}, nil
	})
	if _, err := c.Get("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get("a"); err != nil {
		t.Fatal(err)
	}
	if seen["a"] != 1 || seen["b"] != 1 {
		t.Fatalf("seen = %v, want each account fetched exactly once", seen)
	}
}

func TestCredentialCacheDoesNotCacheAFailure(t *testing.T) {
	wantErr := errors.New("touch id denied")
	attempts := 0
	c := NewCredentialCache(func(string) (map[string]string, error) {
		attempts++
		if attempts == 1 {
			return nil, wantErr
		}
		return map[string]string{"ok": "now"}, nil
	})
	if _, err := c.Get("acct"); !errors.Is(err, wantErr) {
		t.Fatalf("first Get error = %v, want %v", err, wantErr)
	}
	v, err := c.Get("acct")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if v["ok"] != "now" {
		t.Fatalf("second Get = %v, want the retry's value — a failed fetch (e.g. a denied Touch ID prompt) must not poison the cache and lock the account out until Lock/re-launch", v)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (a failure is not cached)", attempts)
	}
}
