// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aiquota/tray/stamp"
)

func goodStamp(at time.Time) stamp.Stamp {
	return stamp.Stamp{Version: stamp.Version, At: at, Verdict: stamp.Available,
		Windows: []stamp.Window{{Account: "Claude Code", Label: "Session (5hr)", Limit: 100, UsedPercent: 10, Verdict: stamp.Available}}}
}

func limitedStamp(retryAfter string) stamp.Stamp {
	return stamp.Stamp{Version: stamp.Version, At: now().UTC(), Verdict: stamp.Unknown,
		Problems: []string{`Claude Code: the usage endpoint is rate limited (Retry-After "` + retryAfter + `")`}}
}

func opts(t *testing.T) sharedOpts {
	return sharedOpts{dir: t.TempDir(), key: "claude||", minInterval: time.Minute, maxStale: 10 * time.Minute, lockWait: 10 * time.Second}
}

func setNow(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

// Twenty callers, one fetch: the others reuse the reading with its real time.
func TestConcurrentCallersShareOneFetch(t *testing.T) {
	setNow(t, t10)
	o := opts(t)
	var fetches atomic.Int32
	fetch := func() stamp.Stamp {
		fetches.Add(1)
		time.Sleep(50 * time.Millisecond) // a slow endpoint: the others must wait for it, not race it
		return goodStamp(t10)
	}
	var wg sync.WaitGroup
	got := make([]stamp.Stamp, 20)
	for i := range got {
		wg.Add(1)
		go func() { defer wg.Done(); got[i] = resolve(o, fetch) }()
	}
	wg.Wait()
	if n := fetches.Load(); n != 1 {
		t.Fatalf("%d fetches for 20 callers, want 1", n)
	}
	for i, s := range got {
		if s.Verdict != stamp.Available || !s.At.Equal(t10) || s.Stale != "" {
			t.Errorf("caller %d: %+v", i, s)
		}
	}
}

func TestReadingIsReusedInsideTheIntervalAndRefetchedAfterIt(t *testing.T) {
	o := opts(t)
	var fetches int
	fetch := func() stamp.Stamp { fetches++; return goodStamp(now()) }
	setNow(t, t10)
	resolve(o, fetch)
	setNow(t, t10.Add(59*time.Second))
	resolve(o, fetch)
	if fetches != 1 {
		t.Fatalf("%d fetches inside the interval", fetches)
	}
	setNow(t, t10.Add(61*time.Second))
	resolve(o, fetch)
	if fetches != 2 {
		t.Fatalf("%d fetches after the interval", fetches)
	}
	// --fresh forces a fetch inside the interval.
	o.fresh = true
	resolve(o, fetch)
	if fetches != 3 {
		t.Fatalf("--fresh did not fetch: %d", fetches)
	}
}

// A 429: the last good reading is served stale, every process honours the wait,
// and nobody hits the endpoint meanwhile (--fresh included).
func TestRetryAfterIsHonouredAcrossProcessesAndServesStale(t *testing.T) {
	o := opts(t)
	setNow(t, t10)
	resolve(o, func() stamp.Stamp { return goodStamp(now()) })

	setNow(t, t10.Add(90*time.Second)) // reading is past the interval
	var fetches int
	limited := func() stamp.Stamp { fetches++; return limitedStamp("120") }
	s := resolve(o, limited)
	if fetches != 1 || s.Verdict != stamp.Available || s.Stale == "" || !s.At.Equal(t10) || !strings.Contains(s.Stale, "rate limited until") {
		t.Fatalf("fetches %d, %+v", fetches, s)
	}
	setNow(t, t10.Add(150*time.Second)) // inside the 120 s wait
	o.fresh = true
	s = resolve(o, limited)
	if fetches != 1 || s.Stale == "" {
		t.Fatalf("the wait was not honoured: %d fetches, %+v", fetches, s)
	}
	setNow(t, t10.Add(215*time.Second)) // the wait is over
	s = resolve(o, func() stamp.Stamp { fetches++; return goodStamp(now()) })
	if fetches != 2 || s.Stale != "" || s.Verdict != stamp.Available {
		t.Fatalf("after the wait: %d fetches, %+v", fetches, s)
	}
}

func TestStaleThenUnknown(t *testing.T) {
	o := opts(t)
	setNow(t, t10)
	resolve(o, func() stamp.Stamp { return goodStamp(now()) })
	limited := func() stamp.Stamp { return limitedStamp("") } // no number: the default wait
	setNow(t, t10.Add(5*time.Minute))
	if s := resolve(o, limited); s.Verdict != stamp.Available || s.Stale == "" {
		t.Fatalf("5 min old: %+v", s)
	}
	setNow(t, t10.Add(11*time.Minute))
	s := resolve(o, limited)
	if s.Verdict != stamp.Unknown || s.Stale != "" || !strings.Contains(strings.Join(s.Problems, " "), "no reading younger than 10m0s") {
		t.Fatalf("11 min old must be UNKNOWN: %+v", s)
	}
}

func TestRateLimitWithNoReadingIsUnknownAndOtherFailuresAreNotDisguised(t *testing.T) {
	o := opts(t)
	setNow(t, t10)
	if s := resolve(o, func() stamp.Stamp { return limitedStamp("2") }); s.Verdict != stamp.Unknown {
		t.Errorf("%+v", s)
	}
	o = opts(t)
	resolve(o, func() stamp.Stamp { return goodStamp(t10) })
	setNow(t, t10.Add(2*time.Minute))
	expired := stamp.Stamp{At: now(), Verdict: stamp.Unknown, Problems: []string{"token expired, open Claude Code"}}
	if s := resolve(o, func() stamp.Stamp { return expired }); s.Verdict != stamp.Unknown || s.Stale != "" {
		t.Errorf("an expired token must not be answered with an old reading: %+v", s)
	}
}

func TestSharingCanBeDisabled(t *testing.T) {
	o := opts(t)
	o.minInterval = 0
	setNow(t, t10)
	var fetches int
	for range 3 {
		resolve(o, func() stamp.Stamp { fetches++; return goodStamp(t10) })
	}
	if fetches != 3 {
		t.Errorf("%d fetches", fetches)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	for in, want := range map[string]time.Duration{"2": 2 * time.Second, "": time.Minute, "0": time.Minute, "99999": time.Hour} {
		if d, ok := rateLimit(limitedStamp(in)); !ok || d != want {
			t.Errorf("Retry-After %q: %v %v, want %v", in, d, ok, want)
		}
	}
	if _, ok := rateLimit(stamp.Stamp{Problems: []string{"token expired"}}); ok {
		t.Error("not a rate limit")
	}
}

func TestBrokenLockOfADeadProcessIsTaken(t *testing.T) {
	dir := t.TempDir()
	path := stamp.CachePath(dir, "k")
	unlock, err := stamp.Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stamp.Lock(path, 100*time.Millisecond); err != stamp.ErrLockTimeout {
		t.Errorf("a held lock must time out: %v", err)
	}
	unlock()
	if u, err := stamp.Lock(path, time.Second); err != nil {
		t.Errorf("released lock: %v", err)
	} else {
		u()
	}
}
