// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package quota

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	hcplugin "github.com/hashicorp/go-plugin"

	aiplugin "github.com/go-aiquota/proto/plugin"
	"github.com/go-aiquota/proto/quotapb"
)

const reexecEnv = "AIQUOTA_TRAY_TEST_SERVE"

// TestMain lets this test binary double as a plugin subprocess, the same
// technique go-aiquota/proto's own plugin_test.go uses to prove a real
// go-plugin round trip without a separate compiled binary.
func TestMain(m *testing.M) {
	if os.Getenv(reexecEnv) == "1" {
		hcplugin.Serve(&hcplugin.ServeConfig{
			HandshakeConfig: aiplugin.Handshake,
			Plugins:         aiplugin.Map(&fakeProvider{}),
			GRPCServer:      hcplugin.DefaultGRPCServer,
		})
		return
	}
	os.Exit(m.Run())
}

// fakeProvider is a minimal QuotaProviderServer: Describe returns a fixed
// ProviderInfo, and FetchQuota echoes the account label back in a canned
// snapshot — enough to prove Manager's plumbing (launch, dial, dispense,
// call, reuse) without a real provider's HTTP logic.
type fakeProvider struct {
	quotapb.UnimplementedQuotaProviderServer
}

func (fakeProvider) Describe(context.Context, *quotapb.DescribeRequest) (*quotapb.ProviderInfo, error) {
	return &quotapb.ProviderInfo{Name: "fake", LoginUrl: "https://example.invalid/login", CookieDomain: "example.invalid"}, nil
}

func (fakeProvider) FetchQuota(_ context.Context, req *quotapb.FetchQuotaRequest) (*quotapb.QuotaSnapshot, error) {
	return &quotapb.QuotaSnapshot{
		AccountLabel: req.AccountLabel,
		PlanKind:     "fake-plan",
		Windows:      []*quotapb.QuotaWindow{{Label: "session", Used: 1, Limit: 10, Unit: "messages"}},
	}, nil
}

// selfAsPlugin is a BinaryLocator that always resolves to this test binary
// re-exec'd in plugin-serve mode, regardless of the provider name asked
// for — every test here exercises exactly one fake provider.
func selfAsPlugin(t *testing.T) BinaryLocator {
	t.Helper()
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return func(string) (string, error) {
		return exePath, nil
	}
}

// newTestManager wires a Manager whose plugin subprocess command resolves
// to this test binary, and registers Close via t.Cleanup.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(selfAsPlugin(t))
	t.Cleanup(m.Close)
	return m
}

// withReexecEnv arms this test process so that any subprocess Manager
// launches (via aiplugin.ClientConfig's exec.Command, which inherits the
// parent's environment by default — nil Cmd.Env means "same as ours") sees
// reexecEnv and serves instead of running tests. Manager builds its own
// *exec.Cmd internally and exposes no seam to set env on it directly, so
// this sets it process-wide for the test's duration instead — matching how
// proto's own plugin_test.go sets it on the cmd.Env it builds by hand.
func withReexecEnv(t *testing.T) {
	t.Helper()
	if err := os.Setenv(reexecEnv, "1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv(reexecEnv) })
}

func TestManagerDescribe(t *testing.T) {
	withReexecEnv(t)
	m := newTestManager(t)

	info, err := m.Describe(context.Background(), "fake")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if info.Name != "fake" || info.LoginUrl == "" || info.CookieDomain == "" {
		t.Fatalf("Describe = %+v, want a populated ProviderInfo", info)
	}
}

func TestManagerFetchQuota(t *testing.T) {
	withReexecEnv(t)
	m := newTestManager(t)

	snap, err := m.FetchQuota(context.Background(), "fake", "acct-1", map[string]string{"session": "poison"})
	if err != nil {
		t.Fatalf("FetchQuota: %v", err)
	}
	if snap.AccountLabel != "acct-1" || len(snap.Windows) != 1 {
		t.Fatalf("FetchQuota = %+v, unexpected shape", snap)
	}
}

func TestManagerReusesClientAcrossCalls(t *testing.T) {
	withReexecEnv(t)
	m := newTestManager(t)

	if _, err := m.FetchQuota(context.Background(), "fake", "a", nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	m.mu.Lock()
	first := m.clients["fake"]
	m.mu.Unlock()

	if _, err := m.FetchQuota(context.Background(), "fake", "b", nil); err != nil {
		t.Fatalf("second call: %v", err)
	}
	m.mu.Lock()
	second := m.clients["fake"]
	m.mu.Unlock()

	if first != second {
		t.Fatal("Manager launched a second subprocess instead of reusing the first")
	}
}

func TestManagerLocatorError(t *testing.T) {
	m := NewManager(func(string) (string, error) {
		return "", errors.New("no such provider")
	})
	t.Cleanup(m.Close)

	if _, err := m.Describe(context.Background(), "missing"); err == nil {
		t.Fatal("Describe with a failing locator: want an error, got nil")
	}
	if _, err := m.FetchQuota(context.Background(), "missing", "acct", nil); err == nil {
		t.Fatal("FetchQuota with a failing locator: want an error, got nil")
	}
}

func TestManagerCloseIsIdempotentAndSafeUnused(t *testing.T) {
	m := NewManager(func(string) (string, error) { return "", errors.New("unused") })
	m.Close()
	m.Close() // must not panic
}

// TestManagerRelaunchesAfterExit covers clientFor's own-state cleanup: a
// client whose subprocess already exited must be relaunched, not reused
// (a crashed plugin should not wedge every later poll for that provider).
func TestManagerRelaunchesAfterExit(t *testing.T) {
	withReexecEnv(t)
	m := newTestManager(t)

	if _, err := m.FetchQuota(context.Background(), "fake", "a", nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	m.mu.Lock()
	first := m.clients["fake"]
	m.mu.Unlock()
	first.Kill() // simulate the subprocess exiting

	if _, err := m.FetchQuota(context.Background(), "fake", "b", nil); err != nil {
		t.Fatalf("call after the subprocess exited: %v", err)
	}
	m.mu.Lock()
	second := m.clients["fake"]
	m.mu.Unlock()
	if first == second {
		t.Fatal("Manager reused a client whose subprocess had already exited")
	}
}

// Sanity: the reexec technique itself, independent of Manager, using
// exec.Command directly — if THIS fails, a Manager test failure means
// nothing about Manager itself.
func TestReexecSanity(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exePath, "-test.run=^TestMain$")
	cmd.Env = append(os.Environ(), reexecEnv+"=1")
	// Serve blocks on stdin/stdout as go-plugin's handshake pipe; just prove
	// the process starts and can be killed cleanly rather than driving a
	// full handshake here (Manager's own tests already do that).
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
