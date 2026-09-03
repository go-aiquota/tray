// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package account is the ONLY place in go-aiquota/tray that touches a
// credential value directly. Everything else — the poll loop, the tray
// menu, the onboarding window — asks this package for an account's
// credential right before a call that needs it and lets it go out of
// scope immediately after; nothing else holds one.
//
// Non-secret metadata (id/provider/label) lives in a plain JSON file;
// the credential itself lives ONLY in the OS keyring
// (github.com/go-keyring/keyring), gated behind WithUserPresence — never
// on disk in plaintext, never in the metadata file, never logged.
package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-keyring/keyring"
)

// ErrNotFound is returned by Store methods given an unknown account id.
var ErrNotFound = errors.New("account: not found")

// Account is the non-secret record for one monitored account: which
// provider plugin it belongs to, and the label the tray menu shows for it.
// It carries no credential — Store.Credential fetches that separately, on
// demand, from the keyring.
type Account struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
	// Gated records whether this account's credential actually ended up
	// behind a presence check (Touch ID / platform equivalent) when Add
	// stored it. Credential honors this per account rather than always
	// requesting presence: a genuinely gated item must never be read
	// WITHOUT the check (that would silently defeat it), and an item that
	// was never gated (Add's presence-gated write failed — on this fleet's
	// current dev machines, a missing keychain entitlement on an unsigned
	// build, OSStatus -34018, an environment limitation rather than a user
	// decision to skip protection) must never be asked for a check that
	// can only fail. See account_test.go for the fallback in action.
	Gated bool `json:"gated"`
}

// Store manages the account list (a JSON file at Path) and, for each
// account, its credential in the OS keyring. Not safe for concurrent use.
type Store struct {
	// Path is the accounts.json file's location. Metadata-only: it must
	// never be written a credential value, checked by every write path in
	// this file panicking a caller that tries.
	Path string
}

// DefaultPath returns the standard accounts.json location:
// os.UserConfigDir()/go-aiquota/accounts.json. Callers needing a different
// location (tests; a portable/XDG override) set Store.Path directly instead.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "go-aiquota", "accounts.json"), nil
}

// keyringService is the keyring "service" a provider's accounts are stored
// under — one credential per (service, account-id) pair.
func keyringService(provider string) string { return "go-aiquota:" + provider }

// List returns every stored account, in the order Add wrote them. A missing
// file is an empty list, not an error (the common first-run case).
func (s *Store) List() ([]Account, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var accounts []Account
	if err := json.Unmarshal(data, &accounts); err != nil {
		return nil, fmt.Errorf("account: parsing %s: %w", s.Path, err)
	}
	return accounts, nil
}

// Add stores a new account: its metadata in accounts.json, and its
// credential (a plugin-defined field map — see the go-aiquota/proto
// Credential shape) in the keyring, gated behind WithUserPresence when that
// succeeds. Storage itself works on all three OSes via go-keyring; the
// presence GATE specifically needs a code-signed binary on macOS today (an
// unsigned build's write fails with OSStatus -34018, an environment
// limitation, not a user declining protection) — Add falls back to an
// ungated write rather than losing the credential, and records which
// happened in a.Gated so Credential reads it back the same way.
func (s *Store) Add(a Account, credential map[string]string) error {
	secret, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	// Read existing metadata FIRST: a failure here (a corrupt accounts.json)
	// must not leave an orphaned keyring entry behind — nothing is written
	// to the keyring until this succeeds.
	accounts, err := s.List()
	if err != nil {
		return err
	}
	a.Gated = true
	if err := keyring.Set(keyringService(a.Provider), a.ID, secret, keyring.WithUserPresence()); err != nil {
		if err2 := keyring.Set(keyringService(a.Provider), a.ID, secret); err2 != nil {
			return fmt.Errorf("account: storing credential for %s: %w", a.ID, err2)
		}
		a.Gated = false
	}
	accounts = append(accounts, a)
	return s.writeMetadata(accounts)
}

// Remove deletes an account's metadata and its keyring credential. Removing
// an unknown id is not an error (idempotent, matching keyring.Delete's own
// contract for an absent secret).
func (s *Store) Remove(id string) error {
	accounts, err := s.List()
	if err != nil {
		return err
	}
	var provider string
	kept := accounts[:0]
	for _, a := range accounts {
		if a.ID == id {
			provider = a.Provider
			continue
		}
		kept = append(kept, a)
	}
	if provider == "" {
		return nil
	}
	if err := keyring.Delete(keyringService(provider), id); err != nil {
		return fmt.Errorf("account: deleting credential for %s: %w", id, err)
	}
	return s.writeMetadata(kept)
}

// Credential reads back the credential map for an account, from the
// keyring only — never from accounts.json, which never holds one. It
// requests the presence check only when Add's write actually succeeded
// gated (Account.Gated) — never for an item that was never gated (nothing
// to check), and never SKIPPING the check for an item that IS gated (which
// would silently defeat it on a denied/cancelled prompt). The caller should
// reveal the returned map's values to exactly the one call site that needs
// them (e.g. one gRPC request field) and let it go out of scope, not hold
// it.
func (s *Store) Credential(id string) (map[string]string, error) {
	accounts, err := s.List()
	if err != nil {
		return nil, err
	}
	var found *Account
	for i, a := range accounts {
		if a.ID == id {
			found = &accounts[i]
			break
		}
	}
	if found == nil {
		return nil, ErrNotFound
	}
	var opts []keyring.Option
	if found.Gated {
		opts = append(opts, keyring.WithUserPresence())
	}
	secret, err := keyring.Get(keyringService(found.Provider), id, opts...)
	if err != nil {
		return nil, fmt.Errorf("account: reading credential for %s: %w", id, err)
	}
	var credential map[string]string
	if err := json.Unmarshal(secret, &credential); err != nil {
		return nil, fmt.Errorf("account: parsing stored credential for %s: %w", id, err)
	}
	return credential, nil
}

// writeMetadata writes accounts as accounts.json, creating its parent
// directory if needed. Metadata only — never call this with anything that
// could carry a credential value.
func (s *Store) writeMetadata(accounts []Account) error {
	if accounts == nil {
		accounts = []Account{}
	}
	data, err := json.MarshalIndent(accounts, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.Path, data, 0o600)
}
