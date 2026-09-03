// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package app

import (
	"context"
	"fmt"
	"time"

	"github.com/go-aiquota/proto/quotapb"
	"github.com/go-aiquota/tray/account"
	"github.com/go-aiquota/tray/menubar"
)

// AccountLister is account.Store's List method, as a seam: a test supplies
// a fake rather than a real accounts.json + keyring.
type AccountLister interface {
	List() ([]account.Account, error)
}

// QuotaFetcher is quota.Manager's FetchQuota method, as a seam: a test
// supplies a fake rather than launching a real plugin subprocess.
type QuotaFetcher interface {
	FetchQuota(ctx context.Context, provider, accountLabel string, credential map[string]string) (*quotapb.QuotaSnapshot, error)
}

// PollAccounts fetches every stored account's current quota snapshot, one
// menubar.AccountStatus per account, in List's own order. Listing failure
// (a corrupt accounts.json) is returned as an error rather than folded into
// a status row — it means the account SET itself is unknown, which is a
// different problem than one account's poll failing, and the caller should
// decide whether to keep showing the last-known statuses rather than
// replace them with nothing.
func PollAccounts(ctx context.Context, lister AccountLister, fetcher QuotaFetcher, cache *CredentialCache, perAccountTimeout time.Duration) ([]menubar.AccountStatus, error) {
	accounts, err := lister.List()
	if err != nil {
		return nil, err
	}
	statuses := make([]menubar.AccountStatus, len(accounts))
	for i, a := range accounts {
		statuses[i] = pollOne(ctx, a, fetcher, cache, perAccountTimeout)
	}
	return statuses, nil
}

// pollOne polls a single account. A credential-cache failure (the keyring
// read itself failed, or a Touch ID prompt was denied/cancelled) and a
// quota-fetch failure both land in AccountStatus.Err — from the tray's
// point of view both mean the same thing, "this account needs attention",
// even though only one of them means "re-authenticate it".
func pollOne(ctx context.Context, a account.Account, fetcher QuotaFetcher, cache *CredentialCache, timeout time.Duration) menubar.AccountStatus {
	status := menubar.AccountStatus{AccountID: a.ID, Label: a.Label}
	cred, err := cache.Get(a.ID)
	if err != nil {
		status.Err = fmt.Errorf("credential: %w", err)
		return status
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snap, err := fetcher.FetchQuota(cctx, a.Provider, a.Label, cred)
	if err != nil {
		status.Err = err
		return status
	}
	status.Snapshot = snap
	return status
}
