// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package account

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-keyring/keyring"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "accounts.json")}
}

// requireKeyring skips a test that needs a real, usable secret store —
// notably true on a headless Linux CI runner with no Secret Service daemon
// (see go-keyring's own documented behavior), matching how that project's
// own tests handle it rather than asserting a specific OS.
func requireKeyring(t *testing.T) {
	t.Helper()
	if !keyring.Available() {
		t.Skip("no usable OS secret store on this host")
	}
}

func TestListEmptyBeforeAnyAdd(t *testing.T) {
	s := newTestStore(t)
	accounts, err := s.List()
	if err != nil {
		t.Fatalf("List on a missing file: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("List = %v, want empty", accounts)
	}
}

// TestListCorruptFileIsAnError covers a real failure mode (a truncated
// write, or a hand-edited file) distinctly from the missing-file case
// above, which is deliberately NOT an error.
func TestListCorruptFileIsAnError(t *testing.T) {
	s := newTestStore(t)
	if err := os.WriteFile(s.Path, []byte("not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); err == nil {
		t.Fatal("List on a corrupt file: want an error, got nil")
	}
}

// TestAddPropagatesAListError covers Add's own error path when the
// existing metadata file can't be read back (so it never even reaches the
// keyring or clobbers the corrupt file).
func TestAddPropagatesAListError(t *testing.T) {
	s := newTestStore(t)
	if err := os.WriteFile(s.Path, []byte("not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := s.Add(Account{ID: "x", Provider: "claude", Label: "X"}, map[string]string{"session": "poison"})
	if err == nil {
		t.Fatal("Add over a corrupt metadata file: want an error, got nil")
	}
}

func TestAddListRemoveMetadataRoundTrip(t *testing.T) {
	requireKeyring(t)
	s := newTestStore(t)

	a := Account{ID: "acct-1", Provider: "claude", Label: "Max personal"}
	if err := s.Add(a, map[string]string{"session": "poison-value-not-a-real-secret"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() { _ = s.Remove(a.ID) })

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Compare the caller-supplied fields only — Gated is decided by Add
	// itself (whether the presence-gated write actually succeeded on THIS
	// host), not something the caller sets; see TestAddRecordsWhetherItGated.
	if len(got) != 1 || got[0].ID != a.ID || got[0].Provider != a.Provider || got[0].Label != a.Label {
		t.Fatalf("List = %+v, want an account matching %+v", got, a)
	}

	if err := s.Remove(a.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, err = s.List()
	if err != nil {
		t.Fatalf("List after Remove: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List after Remove = %v, want empty", got)
	}
}

func TestCredentialRoundTrip(t *testing.T) {
	requireKeyring(t)
	s := newTestStore(t)

	a := Account{ID: "acct-2", Provider: "claude", Label: "Team Premium"}
	want := map[string]string{"session": "poison-value-not-a-real-secret", "org": "acme"}
	if err := s.Add(a, want); err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() { _ = s.Remove(a.ID) })

	got, err := s.Credential(a.ID)
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Credential = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("Credential[%q] = %q, want %q", k, got[k], v)
		}
	}
}

// TestAddRecordsWhetherItGatedAndCredentialAgrees is environment-agnostic on
// purpose: it does not assert a specific Gated value (this dev machine has
// no code-signing identity, so the presence-gated write fails and Add falls
// back — see store.go's doc comment — but a signed build, or a future CI
// runner, could genuinely succeed gated). What it DOES assert: whatever
// Add decided, List reports it and Credential can read the value back
// through that SAME decision — the actual load-bearing property (read mode
// tracks write mode, so a gated item is never read without the check and an
// ungated one is never asked for a check that can only fail).
func TestAddRecordsWhetherItGatedAndCredentialAgrees(t *testing.T) {
	requireKeyring(t)
	s := newTestStore(t)

	a := Account{ID: "acct-gated-check", Provider: "claude", Label: "Gate check"}
	want := map[string]string{"session": "poison-gate-check"}
	if err := s.Add(a, want); err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() { _ = s.Remove(a.ID) })

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List = %v, want one account", got)
	}
	t.Logf("Gated = %v on this host", got[0].Gated)

	cred, err := s.Credential(a.ID)
	if err != nil {
		t.Fatalf("Credential (Gated=%v): %v", got[0].Gated, err)
	}
	if cred["session"] != want["session"] {
		t.Fatalf("Credential = %v, want %v", cred, want)
	}
}

func TestCredentialUnknownAccountIsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Credential("nope"); err != ErrNotFound {
		t.Fatalf("Credential(unknown) = %v, want ErrNotFound", err)
	}
}

func TestRemoveUnknownAccountIsNoop(t *testing.T) {
	s := newTestStore(t)
	if err := s.Remove("nope"); err != nil {
		t.Fatalf("Remove(unknown): %v, want nil", err)
	}
}

func TestRemoveOnlyDeletesTheMatchingAccount(t *testing.T) {
	requireKeyring(t)
	s := newTestStore(t)

	a1 := Account{ID: "acct-a", Provider: "claude", Label: "A"}
	a2 := Account{ID: "acct-b", Provider: "claude", Label: "B"}
	if err := s.Add(a1, map[string]string{"session": "poison-a"}); err != nil {
		t.Fatalf("Add a1: %v", err)
	}
	if err := s.Add(a2, map[string]string{"session": "poison-b"}); err != nil {
		t.Fatalf("Add a2: %v", err)
	}
	t.Cleanup(func() { _ = s.Remove(a1.ID); _ = s.Remove(a2.ID) })

	if err := s.Remove(a1.ID); err != nil {
		t.Fatalf("Remove a1: %v", err)
	}
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != a2.ID || got[0].Provider != a2.Provider || got[0].Label != a2.Label {
		t.Fatalf("List after removing a1 = %+v, want an account matching %+v", got, a2)
	}
	if _, err := s.Credential(a2.ID); err != nil {
		t.Fatalf("a2's credential should survive a1's removal: %v", err)
	}
}

func TestDefaultPathEndsInAppNameAndFile(t *testing.T) {
	p, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if filepath.Base(p) != "accounts.json" {
		t.Fatalf("DefaultPath = %q, want it to end in accounts.json", p)
	}
	if filepath.Base(filepath.Dir(p)) != "go-aiquota" {
		t.Fatalf("DefaultPath = %q, want its directory to be go-aiquota", p)
	}
}
