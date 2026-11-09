// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/go-aiquota/tray/stamp"
)

// One reading for every aiquota process. The usage endpoint rate limits, and
// the machine runs several editor windows each polling it, plus the CLI and
// the quotaclaim gate. resolve makes them share: within minInterval a process
// reuses the last good reading (with its real measurement time), only one
// process at a time fetches (a lock file next to the stamp), a Retry-After is
// written down and honoured by all, and while the endpoint cannot be asked the
// last good reading is served marked stale for up to maxStale, after which
// the answer is UNKNOWN.

type sharedOpts struct {
	dir         string        // where the stamp lives; the cache is next to it
	key         string        // provider|account|view
	minInterval time.Duration // reuse a reading younger than this; <= 0 disables sharing
	maxStale    time.Duration // longest a reading may be served stale
	fresh       bool          // fetch even inside minInterval (Retry-After still holds)
	lockWait    time.Duration // longest to wait for another process's fetch
}

var retryAfterRe = regexp.MustCompile(`rate limited \(Retry-After "(\d*)"\)`)

const (
	defaultRetryAfter = time.Minute
	maxRetryAfter     = time.Hour
)

// rateLimit reports whether the measurement failed because the endpoint rate
// limited it, and for how long it asked to wait.
func rateLimit(st stamp.Stamp) (time.Duration, bool) {
	for _, p := range st.Problems {
		m := retryAfterRe.FindStringSubmatch(p)
		if m == nil {
			continue
		}
		d := defaultRetryAfter
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			d = time.Duration(n) * time.Second
		}
		return min(d, maxRetryAfter), true
	}
	return 0, false
}

func good(st stamp.Stamp) bool { return st.Verdict == stamp.Available || st.Verdict == stamp.Exhausted }

// resolve returns the shared reading, calling fetch (which measures live) only
// when no process has a reading young enough and the endpoint may be asked.
func resolve(o sharedOpts, fetch func() stamp.Stamp) stamp.Stamp {
	if o.minInterval <= 0 {
		return fetch()
	}
	path := stamp.CachePath(o.dir, o.key)
	reusable := func(e stamp.CacheEntry, at time.Time) bool {
		return !o.fresh && e.HasReading() && at.Sub(e.Reading.At) < o.minInterval
	}
	if e := stamp.ReadCache(path); reusable(e, now()) {
		return e.Reading
	}
	unlock, err := stamp.Lock(path, o.lockWait)
	if err != nil {
		// Another process has been fetching for the whole wait: serve what is held.
		return serveHeld(stamp.ReadCache(path), now(), o, "another aiquota process is still fetching the shared reading")
	}
	defer unlock()

	e := stamp.ReadCache(path) // another process may have fetched while this one waited
	at := now()
	if reusable(e, at) {
		return e.Reading
	}
	if e.NotBefore.After(at) {
		return serveHeld(e, at, o, "the usage endpoint is rate limited until "+e.NotBefore.Local().Format("15:04:05"))
	}
	st := fetch()
	if good(st) {
		_ = stamp.WriteCache(path, stamp.CacheEntry{Reading: st})
		return st
	}
	if wait, limited := rateLimit(st); limited {
		e.NotBefore = now().Add(wait)
		_ = stamp.WriteCache(path, e)
		return serveHeld(e, now(), o, "the usage endpoint is rate limited until "+e.NotBefore.Local().Format("15:04:05"))
	}
	return st
}

// serveHeld serves the last good reading marked stale while it is within
// maxStale, and UNKNOWN once it is not (or when there is none).
func serveHeld(e stamp.CacheEntry, at time.Time, o sharedOpts, why string) stamp.Stamp {
	if e.HasReading() && at.Sub(e.Reading.At) <= o.maxStale {
		s := e.Reading
		s.Stale = fmt.Sprintf("%s; this is the reading of %s (%s old)", why, s.At.Local().Format("15:04:05"), at.Sub(s.At).Round(time.Second))
		return s
	}
	return unreadable(fmt.Sprintf("%s, and no reading younger than %s is held", why, o.maxStale))
}
