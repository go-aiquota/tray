// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package app wires account.Store, quota.Manager and menubar together into
// the poll loop the tray runs — the parts of the application that don't
// touch an OS window or a native tray backend, and so can be exercised with
// fakes instead of real hardware. The OS-facing glue (opening the actual
// tray icon, driving the platform run loop, opening the onboarding window)
// lives in cmd/aiquota-tray, per [[go-webengine-interactive-form-input]]'s
// own precedent for where a "launch-verified boundary" belongs.
package app

import "sync"

// CredentialCache holds a per-process, in-memory copy of each account's
// credential, fetched at most once per Lock: a Gated (Touch-ID-protected)
// account should prompt once when the tray starts watching it, not once per
// poll interval — a person is not expected to authenticate every five
// minutes just because the tray is checking quota in the background.
//
// Lock is what gives that gate a reason to exist again once a copy is
// sitting in memory: without it, nothing in this process ever asks for
// presence a second time, which would make "Gated" a one-time formality
// rather than an ongoing protection. Calling Lock is the tray's "Lock now"
// action.
type CredentialCache struct {
	mu     sync.Mutex
	get    func(id string) (map[string]string, error)
	values map[string]map[string]string
}

// NewCredentialCache builds a cache that fetches a miss through get — the
// shape of account.Store.Credential, taken as a func rather than an
// interface so a test can supply one without a fake Store.
func NewCredentialCache(get func(id string) (map[string]string, error)) *CredentialCache {
	return &CredentialCache{get: get, values: map[string]map[string]string{}}
}

// Get returns id's credential, fetching and caching it on the first call
// (per account, since the last Lock).
func (c *CredentialCache) Get(id string) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.values[id]; ok {
		return v, nil
	}
	v, err := c.get(id)
	if err != nil {
		return nil, err
	}
	c.values[id] = v
	return v, nil
}

// Lock discards every cached credential. The next Get for any account
// fetches (and, for a Gated account, prompts) again.
func (c *CredentialCache) Lock() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values = map[string]map[string]string{}
}
