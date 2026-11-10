// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package stamp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// The shared reading. Every aiquota process of the machine (several editor
// windows, the CLI, the quotaclaim gate) asks the same usage endpoint, which
// rate limits. They therefore share ONE reading: the last good one, and a
// "not before" time when the endpoint said to wait. It sits next to the stamp
// and holds, like it, no credential.

// CacheEntry is the shared reading of one (provider, account, view).
type CacheEntry struct {
	Version int `json:"version"`
	// Reading is the last GOOD measurement (available or exhausted); the zero
	// Stamp when there is none.
	Reading Stamp `json:"reading"`
	// NotBefore is the earliest time the endpoint may be asked again (a
	// Retry-After, honoured across processes); zero when there is no wait.
	NotBefore time.Time `json:"not_before,omitzero"`
}

// HasReading reports whether Reading is a usable (good) measurement.
func (e CacheEntry) HasReading() bool {
	return !e.Reading.At.IsZero() && (e.Reading.Verdict == Available || e.Reading.Verdict == Exhausted)
}

// CachePath is the cache file for key inside dir.
func CachePath(dir, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, "cache-"+hex.EncodeToString(sum[:4])+".json")
}

// ReadCache loads the entry at path; a missing or unreadable file is an empty
// entry (a cache that cannot be read is a cache miss, never an error).
func ReadCache(path string) CacheEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return CacheEntry{}
	}
	var e CacheEntry
	if json.Unmarshal(data, &e) != nil {
		return CacheEntry{}
	}
	return e
}

// WriteCache stores e at path atomically, mode 0600 in a 0700 directory.
func WriteCache(path string, e CacheEntry) error {
	e.Version = Version
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".cache-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// lockStale is how old a lock file must be before it is taken to be a dead
// process's and removed.
const lockStale = 2 * time.Minute

// ErrLockTimeout means another process held the lock for the whole wait.
var ErrLockTimeout = errors.New("stamp: the shared reading is being fetched by another process")

// Lock takes the exclusive lock next to path (a file created with O_EXCL, so it
// works on every platform), waiting up to wait for its holder. It returns the
// release function. A lock older than lockStale is a crashed holder's and is
// broken.
func Lock(path string, wait time.Duration) (func(), error) {
	lock := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(lock) }, nil
		}
		// On Windows a lock file its holder has just removed stays "delete
		// pending" for a moment, and creating it then fails with access denied
		// rather than "exists": that is contention too.
		contended := errors.Is(err, os.ErrExist) || (runtime.GOOS == "never" && errors.Is(err, os.ErrPermission)) // CONTROL: the old behaviour
		if !contended {
			return nil, err
		}
		if fi, serr := os.Stat(lock); serr == nil && time.Since(fi.ModTime()) > lockStale {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return nil, ErrLockTimeout
		}
		time.Sleep(25 * time.Millisecond)
	}
}
