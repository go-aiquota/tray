// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Freshness without a manual `aiquota status`: when the hook finds the stamp
// missing or older than --max-age it refreshes it itself, bounded three ways.
//
//   - single flight: one process at a time holds a lock file (O_EXCL) next to
//     the state file; the others read the old stamp;
//   - bounded: the hook waits --refresh-timeout for `aiquota status`, then goes
//     on with whatever stamp exists; the child is not killed, it finishes and
//     writes the stamp for the next spawn;
//   - throttled: the attempt time is written BEFORE the query, so at most one
//     live query per --refresh-every is made even if every one fails.

// lockTTL is how long a lock of a crashed holder is honoured.
const lockTTL = 30 * time.Second

type refreshState struct {
	LastAttempt time.Time `json:"last_attempt"`
}

// Seams the tests replace.
var (
	startRefresh = execRefresh
	takeLock     = lockFile
	lockClock    = time.Now // the clock a lock's age is read against (file times are real)
)

// findProgram resolves the refresher: the named program, or `aiquota` in the
// directory beside, or on PATH.
func findProgram(prog, beside string) (string, error) {
	if prog != "" {
		return exec.LookPath(prog)
	}
	if p, err := exec.LookPath(filepath.Join(beside, "aiquota")); err == nil {
		return p, nil
	}
	return exec.LookPath("aiquota")
}

// execRefresh runs `prog args` and waits at most wait. A non-zero exit is not
// an error here (aiquota status exits 1 or 3 for an exhausted or unknown quota
// but still writes the stamp); the caller re-reads the stamp.
func execRefresh(prog string, args []string, wait time.Duration) error {
	exe, _ := os.Executable() // "" on failure: the lookup then tries the working directory, then PATH
	path, lookErr := findProgram(prog, filepath.Dir(exe))
	cmd := exec.Command(path, args...)
	if lookErr != nil {
		return lookErr
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil
		}
		return err
	case <-time.After(wait):
		return fmt.Errorf("%s still running after %s, left to finish", filepath.Base(path), wait)
	}
}

// refreshDue reports whether the last attempt is at least --refresh-every ago
// (or unknown, or in the future: a clock that went back must not block forever).
func (o *opts) refreshDue(at time.Time) bool {
	var s refreshState
	data, err := os.ReadFile(o.p.refresh)
	if err != nil || json.Unmarshal(data, &s) != nil {
		return true
	}
	el := at.Sub(s.LastAttempt)
	return el >= o.refreshEvery || el < 0
}

// lockFile takes an exclusive lock file, reclaiming one older than lockTTL. The
// release func is always callable.
func lockFile(path string) (release func(), ok bool) {
	_ = os.MkdirAll(filepath.Dir(path), 0o700) // a failure shows as the open below failing
	for range 2 {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }, true
		}
		if fi, err := os.Stat(path); err == nil {
			if lockClock().Sub(fi.ModTime()) < lockTTL {
				break // another process is refreshing
			}
			os.Remove(path) // a crashed holder's
		}
	}
	return func() {}, false
}

// refresh asks aiquota for a new stamp when one is due, free to run and
// allowed. It never fails the hook: every problem is said on stderr.
func (o *opts) refresh(at time.Time) {
	if o.refreshEvery <= 0 || o.p.refresh == "" || !o.refreshDue(at) {
		return
	}
	unlock, ok := takeLock(o.p.refresh + ".lock")
	defer unlock()
	if !ok || !o.refreshDue(at) { // or another process attempted while this one raced for the lock
		return
	}
	data, _ := json.Marshal(refreshState{LastAttempt: at})
	if err := writeFileIn(o.p.refresh, data, os.O_WRONLY|os.O_CREATE|os.O_TRUNC); err != nil {
		fmt.Fprintln(o.warn, "quotapace: not refreshing, cannot record the attempt:", err)
		return
	}
	if err := startRefresh(o.refreshCmd, []string{"status", "--stamp", o.p.stamp}, o.refreshTimeout); err != nil {
		fmt.Fprintln(o.warn, "quotapace: stamp refresh:", err)
	}
}
