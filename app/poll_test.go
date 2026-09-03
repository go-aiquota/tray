// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-aiquota/proto/quotapb"
	"github.com/go-aiquota/tray/account"
)

type fakeLister struct {
	accounts []account.Account
	err      error
}

func (f fakeLister) List() ([]account.Account, error) { return f.accounts, f.err }

type fakeFetcher struct {
	// byLabel maps an account label to either a snapshot or an error.
	snapshots map[string]*quotapb.QuotaSnapshot
	errs      map[string]error
}

func (f fakeFetcher) FetchQuota(_ context.Context, _, accountLabel string, _ map[string]string) (*quotapb.QuotaSnapshot, error) {
	if err, ok := f.errs[accountLabel]; ok {
		return nil, err
	}
	return f.snapshots[accountLabel], nil
}

func TestPollAccountsListError(t *testing.T) {
	wantErr := errors.New("accounts.json is corrupt")
	cache := NewCredentialCache(func(string) (map[string]string, error) { return nil, nil })
	_, err := PollAccounts(context.Background(), fakeLister{err: wantErr}, fakeFetcher{}, cache, time.Second)
	if !errors.Is(err, wantErr) {
		t.Fatalf("PollAccounts error = %v, want %v", err, wantErr)
	}
}

func TestPollAccountsEmpty(t *testing.T) {
	cache := NewCredentialCache(func(string) (map[string]string, error) { return nil, nil })
	statuses, err := PollAccounts(context.Background(), fakeLister{}, fakeFetcher{}, cache, time.Second)
	if err != nil {
		t.Fatalf("PollAccounts: %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("statuses = %v, want empty for no accounts", statuses)
	}
}

func TestPollAccountsSuccessAndFailureMix(t *testing.T) {
	accounts := []account.Account{
		{ID: "a1", Provider: "claude", Label: "work@example.com"},
		{ID: "a2", Provider: "claude", Label: "broken@example.com"},
	}
	snap := &quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{{Label: "session", Used: 10, Limit: 100}}}
	fetchErr := errors.New("session expired")
	cache := NewCredentialCache(func(id string) (map[string]string, error) {
		return map[string]string{"cookie": "tok-" + id}, nil
	})
	fetcher := fakeFetcher{
		snapshots: map[string]*quotapb.QuotaSnapshot{"work@example.com": snap},
		errs:      map[string]error{"broken@example.com": fetchErr},
	}

	statuses, err := PollAccounts(context.Background(), fakeLister{accounts: accounts}, fetcher, cache, time.Second)
	if err != nil {
		t.Fatalf("PollAccounts: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("len(statuses) = %d, want 2", len(statuses))
	}
	if statuses[0].AccountID != "a1" || statuses[0].Err != nil || statuses[0].Snapshot != snap {
		t.Fatalf("statuses[0] = %+v, want a1's snapshot with no error", statuses[0])
	}
	if statuses[1].AccountID != "a2" || !errors.Is(statuses[1].Err, fetchErr) {
		t.Fatalf("statuses[1] = %+v, want a2's fetch error", statuses[1])
	}
}

func TestPollAccountsCredentialFailureIsAnAccountError(t *testing.T) {
	accounts := []account.Account{{ID: "a1", Provider: "claude", Label: "locked@example.com"}}
	credErr := errors.New("touch id denied")
	cache := NewCredentialCache(func(string) (map[string]string, error) { return nil, credErr })

	statuses, err := PollAccounts(context.Background(), fakeLister{accounts: accounts}, fakeFetcher{}, cache, time.Second)
	if err != nil {
		t.Fatalf("PollAccounts: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Err == nil || !errors.Is(statuses[0].Err, credErr) {
		t.Fatalf("statuses = %+v, want one row carrying the credential error", statuses)
	}
	if statuses[0].Snapshot != nil {
		t.Fatalf("statuses[0].Snapshot = %v, want nil when the credential fetch itself failed", statuses[0].Snapshot)
	}
}

// slowFetcher never returns until its context is done, so a poll of it
// proves PollAccounts actually bounds each account's fetch with
// perAccountTimeout rather than letting one unreachable account hang the
// whole cycle.
type slowFetcher struct{}

func (slowFetcher) FetchQuota(ctx context.Context, _, _ string, _ map[string]string) (*quotapb.QuotaSnapshot, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestPollAccountsPerAccountTimeout(t *testing.T) {
	accounts := []account.Account{{ID: "a1", Provider: "claude", Label: "slow@example.com"}}
	cache := NewCredentialCache(func(string) (map[string]string, error) { return map[string]string{}, nil })

	start := time.Now()
	statuses, err := PollAccounts(context.Background(), fakeLister{accounts: accounts}, slowFetcher{}, cache, 50*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("PollAccounts: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("PollAccounts against a hanging fetcher took %v, want it bounded by perAccountTimeout", elapsed)
	}
	if len(statuses) != 1 || statuses[0].Err == nil {
		t.Fatalf("statuses = %+v, want a1's row to carry the timeout error", statuses)
	}
}
