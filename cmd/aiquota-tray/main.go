// Command aiquota-tray is a menu-bar app that watches AI usage quota across
// several accounts (multiple Claude Max accounts, Claude Team Premium,
// Claude Team Standard, ...) in parallel, through per-provider
// hashicorp/go-plugin subprocesses.
//
// It never asks for a cookie to be copied out of a real browser: "Add
// account" opens an isolated, in-process login window (go-aiquota/tray's
// onboarding package, built on go-webengine) and stores whatever the
// provider's own site sets as a cookie — nothing this program invents or
// guesses the shape of.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/go-aiquota/tray/account"
	"github.com/go-aiquota/tray/app"
	"github.com/go-aiquota/tray/menubar"
	"github.com/go-aiquota/tray/quota"
	"github.com/go-widgets/mvvm"
)

// init locks the main goroutine to the process's main OS thread before
// anything else runs. Both the tray backend and the onboarding window need
// to create native objects (NSStatusItem, NSWindow) on that exact thread,
// and locking it later — after some other call has already let the
// goroutine migrate — is an intermittent failure, not a reliable one; see
// go-xrkit/desk/cmd/xrdesk's own init for the incident that established
// this pattern.
func init() { runtime.LockOSThread() }

const (
	// provider names the one plugin this build ships with. Adding
	// ChatGPT/Gemini later means adding entries here (and their own
	// go-aiquota/plugin-* binaries) — see the proto package's own doc
	// comment for why the plugin boundary exists at all.
	provider = "claude"
	// loginURL is claude.ai's own public sign-in page — not a secret, and
	// not the (still unknown) internal usage-quota endpoint go-aiquota's
	// own plugin will eventually call; onboarding only ever drives this
	// page, never that endpoint.
	loginURL = "https://claude.ai/login"
	// cookieDomain is what the onboarding step is asked for cookies of
	// after a login: the WHOLE cookie jar for the domain, not one named
	// cookie — this program does not know which cookie claude.ai actually
	// uses for a session, and guessing its name would be worse than
	// storing a few extra harmless ones. The plugin that reads them back
	// decides which it needs.
	cookieDomain = "claude.ai"

	// orgUUIDCredentialKey is the one non-cookie field plugin-claude's
	// FetchQuota needs alongside the cookie jar (see its own provider.go
	// for why: there is no page a scripted client can hit to rediscover an
	// org's UUID from cookies alone). Duplicated as a literal in both
	// repos deliberately, not hoisted into go-aiquota/proto's shared
	// contract — it is a claude-specific extension of Credential, per that
	// message's own doc comment ("or other credential fields for a future
	// non-cookie provider"), not part of the generic host/plugin contract
	// every provider shares. Keep this in sync with plugin-claude's own
	// orgUUIDKey if either changes.
	orgUUIDCredentialKey = "org_uuid"

	pollInterval  = 5 * time.Minute
	pollTimeout   = 20 * time.Second
	onboardWidth  = 480
	onboardHeight = 640
)

func main() { os.Exit(run()) }

func run() int {
	path, err := account.DefaultPath()
	if err != nil {
		log.Printf("aiquota-tray: %v", err)
		return 1
	}
	store := &account.Store{Path: path}
	manager := quota.NewManager(locateBinary)
	defer manager.Close()
	cache := app.NewCredentialCache(store.Credential)

	actions := make(chan app.Action, 4)
	send := func(a app.Action) {
		select {
		case actions <- a:
		default:
			log.Printf("aiquota-tray: an action was chosen while the tray was not listening; dropped")
		}
	}

	thresholds := menubar.DefaultThresholds
	state := mvvm.NewObservable(menubar.SeverityUnknown)

	// Shared by the control item's own menu AND every per-account item's
	// menu (see AccountItems) — one set of callbacks, so "Quit" or "Add
	// account…" does the same thing no matter which icon a person clicked.
	sharedActions := menubar.Actions{
		AddAccount: func() { send(app.ActionAddAccount) },
		LockNow:    func() { send(app.ActionLockNow) },
		Quit:       func() { send(app.ActionQuit) },
	}

	item, err := menubar.Open(state, menubar.BuildMenu(nil, thresholds, sharedActions))
	if err != nil {
		log.Printf("aiquota-tray: %v", err)
		return 1
	}
	defer func() { _ = item.Close() }()

	// One tray item PER ACCOUNT, showing its usage as text directly in the
	// menu bar (a Stats-style display) — Attached to the SAME platform loop
	// the control item above Holds. Sync must not run until that loop
	// actually exists (see menubar.Tray.OnReady's own doc comment), which
	// is why refresh (below) is wired to OnReady rather than called here
	// directly.
	accountItems := menubar.NewAccountItems(sharedActions, thresholds)
	defer accountItems.Close()

	// refresh builds every menu straight from THIS call's own statuses
	// rather than a shared variable read back later: it is called from more
	// than one goroutine (the ticker below, and whatever goroutine handles
	// OnAddAccount), and a shared mutable "current accounts" slice would be
	// a data race between two such calls with nothing here to prevent it.
	refresh := func() {
		ctx, cancel := context.WithTimeout(context.Background(), pollInterval)
		defer cancel()
		statuses, err := app.PollAccounts(ctx, store, manager, cache, pollTimeout)
		if err != nil {
			// Nothing is applied: whatever the tray last showed stays,
			// rather than being wiped by a transient accounts.json read
			// hiccup.
			log.Printf("aiquota-tray: polling accounts: %v", err)
			return
		}
		state.Set(menubar.Aggregate(statuses, thresholds))
		item.SetMenu(menubar.BuildMenu(statuses, thresholds, sharedActions))
		if err := accountItems.Sync(statuses); err != nil {
			log.Printf("aiquota-tray: syncing per-account tray items: %v", err)
		}
	}
	// Backgrounded, not called directly: OnReady fires SYNCHRONOUSLY, on the
	// main thread, from INSIDE the same call that creates the status item
	// and BEFORE Hold's Run reaches -[NSApplication finishLaunching] or
	// starts pumping the run loop at all. Measured: calling refresh()
	// (network I/O — a plugin subprocess launch, a gRPC handshake, an HTTPS
	// fetch) directly here delayed that by long enough that the status
	// item never actually appeared in the real menu bar (confirmed absent
	// from the accessibility tree too, not just visually crowded out) —
	// even with per-account items disabled, so it was this delay itself,
	// not anything about them. A goroutine lets Run reach finishLaunching
	// and start the loop immediately; the first real refresh lands moments
	// later, once something is actually there to update.
	item.OnReady(func() { go refresh() })

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	stopPoll := make(chan struct{})
	defer close(stopPoll)
	go func() {
		for {
			select {
			case <-ticker.C:
				refresh()
			case <-stopPoll:
				return
			}
		}
	}()

	app.Loop(item, actions, app.Handlers{
		OnHoldError: func(err error) { log.Printf("aiquota-tray: tray: %v", err) },
		OnLockNow:   cache.Lock,
		OnAddAccount: func() {
			if err := addAccount(store); err != nil {
				log.Printf("aiquota-tray: adding account: %v", err)
			}
			refresh()
		},
	})
	return 0
}

// addAccount opens the onboarding window (see openOnboarding, which picks
// the platform-appropriate mechanism), waits for it to close, and — if the
// site actually set a cookie — stores the new account. A closed window
// with no cookie (the person cancelled, or the login failed) adds nothing;
// that's not an error worth logging as one.
func addAccount(store *account.Store) error {
	cookies, err := openOnboarding(loginURL, cookieDomain, onboardWidth, onboardHeight)
	if err != nil {
		return err
	}
	if len(cookies) == 0 {
		return nil
	}
	id, err := newAccountID()
	if err != nil {
		return err
	}
	// Label starts as the id: nothing in this window flow captures a
	// human-chosen name (there is no text-input dialog for one yet), and
	// guessing one out of the page (an email field's value, say) would be
	// provider-specific in a package that deliberately isn't. A rename
	// affordance is a reasonable follow-up, not a blocker for storing the
	// account at all.
	a := account.Account{ID: id, Provider: provider, Label: id}
	return store.Add(a, cookies)
}

// newAccountID returns a short random hex id — unique enough for a
// per-machine account list that a person will have a handful of entries
// in, not thousands.
func newAccountID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating an account id: %w", err)
	}
	return "acct-" + hex.EncodeToString(b[:]), nil
}

// locateBinary finds a provider's plugin binary on PATH, named
// go-aiquota-plugin-<provider> (e.g. go-aiquota-plugin-claude) — the
// hashicorp/go-plugin subprocess quota.Manager launches and talks to over
// gRPC. Not finding one is reported through the normal per-account error
// path (an AccountStatus.Err in the tray menu), not a startup failure: a
// build with only the claude plugin installed should still show every
// OTHER provider's accounts as broken rather than refuse to run.
func locateBinary(provider string) (string, error) {
	name := "go-aiquota-plugin-" + provider
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("no %s on PATH: %w", name, err)
	}
	return path, nil
}
