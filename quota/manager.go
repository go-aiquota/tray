// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package quota launches and talks to provider plugins over the
// go-aiquota/proto gRPC contract: one go-plugin subprocess per provider,
// reused across polls, torn down on Close. It never touches the OS keyring
// or accounts.json directly — the caller (the poll loop) resolves a
// credential via account.Store right before a call that needs it and hands
// it in as a plain argument; Manager does not hold one longer than that
// call.
package quota

import (
	"context"
	"fmt"
	"sync"

	hcplugin "github.com/hashicorp/go-plugin"

	aiplugin "github.com/go-aiquota/proto/plugin"
	"github.com/go-aiquota/proto/quotapb"
)

// BinaryLocator resolves a provider name (e.g. "claude") to the path of its
// plugin executable. The host supplies this — Manager has no opinion on
// where plugin binaries live (PATH, a bundled directory next to the tray
// binary, …).
type BinaryLocator func(provider string) (path string, err error)

// Manager owns one go-plugin client per provider, launched lazily on first
// use and reused across calls (a provider's subprocess is not restarted
// per-account or per-poll). Not safe for concurrent use across goroutines
// without external synchronization beyond what's documented per method —
// see FetchQuota/Describe, which ARE safe to call concurrently for
// different providers (each provider's client is independent) but serialize
// per provider.
type Manager struct {
	locate BinaryLocator

	mu      sync.Mutex
	clients map[string]*hcplugin.Client
}

// NewManager returns a Manager that resolves provider binaries via locate.
func NewManager(locate BinaryLocator) *Manager {
	return &Manager{locate: locate, clients: map[string]*hcplugin.Client{}}
}

// Describe returns a provider's static info (its onboarding login URL and
// cookie domain) — what the onboarding window needs before any account of
// that provider exists yet.
func (m *Manager) Describe(ctx context.Context, provider string) (*quotapb.ProviderInfo, error) {
	c, err := m.clientFor(provider)
	if err != nil {
		return nil, err
	}
	return c.Describe(ctx, &quotapb.DescribeRequest{})
}

// FetchQuota calls the provider's FetchQuota for one account. credential is
// passed straight through to the plugin over go-plugin's AutoMTLS channel;
// Manager itself never logs or stores it.
func (m *Manager) FetchQuota(ctx context.Context, provider, accountLabel string, credential map[string]string) (*quotapb.QuotaSnapshot, error) {
	c, err := m.clientFor(provider)
	if err != nil {
		return nil, err
	}
	return c.FetchQuota(ctx, &quotapb.FetchQuotaRequest{
		AccountLabel: accountLabel,
		Credential:   credential,
	})
}

// clientFor returns a live QuotaProviderClient for provider, launching its
// plugin subprocess on first use (or relaunching it if the previous one
// exited — a plugin that crashed mid-run should not wedge every later poll
// for that provider).
func (m *Manager) clientFor(provider string) (quotapb.QuotaProviderClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	c, ok := m.clients[provider]
	if !ok || c.Exited() {
		path, err := m.locate(provider)
		if err != nil {
			return nil, fmt.Errorf("quota: locating %s plugin: %w", provider, err)
		}
		cfg := aiplugin.ClientConfig(path)
		c = hcplugin.NewClient(&cfg)
		m.clients[provider] = c
	}

	rpcClient, err := c.Client()
	if err != nil {
		delete(m.clients, provider) // don't cache a client stuck in a bad state
		return nil, fmt.Errorf("quota: connecting to %s plugin: %w", provider, err)
	}
	raw, err := rpcClient.Dispense(aiplugin.Key)
	if err != nil {
		return nil, fmt.Errorf("quota: dispensing %s plugin: %w", provider, err)
	}
	qc, ok := raw.(quotapb.QuotaProviderClient)
	if !ok {
		return nil, fmt.Errorf("quota: %s plugin returned %T, want quotapb.QuotaProviderClient", provider, raw)
	}
	return qc, nil
}

// Close terminates every launched plugin subprocess. Safe to call once,
// when the host is shutting down or reconfiguring its provider set.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		c.Kill()
	}
	m.clients = map[string]*hcplugin.Client{}
}
