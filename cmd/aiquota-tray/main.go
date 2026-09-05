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
	"strings"
	"time"

	"github.com/go-aiquota/proto/quotapb"
	"github.com/go-aiquota/tray/account"
	"github.com/go-aiquota/tray/app"
	"github.com/go-aiquota/tray/history"
	"github.com/go-aiquota/tray/menubar"
	"github.com/go-aiquota/tray/quota"
	"github.com/go-widgets/mvvm"
	"github.com/go-widgets/toolkit"
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
	// Anti-aliased, shaped text (the bundled Atkinson Hyperlegible face)
	// instead of toolkit's built-in 5x7 bitmap font, for every window this
	// process opens (onboarding, history charts, ...). A parse failure
	// leaves the bitmap default in place rather than blocking startup.
	if err := toolkit.UseOpenTypeText(); err != nil {
		log.Printf("aiquota-tray: falling back to the bitmap font: %v", err)
	}

	path, err := account.DefaultPath()
	if err != nil {
		log.Printf("aiquota-tray: %v", err)
		return 1
	}
	store := &account.Store{Path: path}
	manager := quota.NewManager(locateBinary)
	defer manager.Close()
	cache := app.NewCredentialCache(store.Credential)

	histPath, err := history.DefaultPath()
	if err != nil {
		log.Printf("aiquota-tray: %v", err)
		return 1
	}
	histStore := &history.Store{Path: histPath}

	// Discovered ONCE at startup, not re-scanned per menu-open: a plugin
	// binary appearing on PATH mid-run is rare enough that requiring a
	// restart to pick it up is a reasonable v1 trade for not re-launching
	// every provider's subprocess on every "Add account" click. See
	// quota.DiscoverProviders' own doc comment — a plugin needs nothing
	// beyond existing on PATH under the right name to show up here; this
	// program has no per-provider code to add.
	providerInfo, providerChoices := discoverProviders(manager)

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
		AddAccount:  func(provider string) { send(app.Action{Kind: app.ActionAddAccount, Provider: provider}) },
		LockNow:     func() { send(app.Action{Kind: app.ActionLockNow}) },
		Quit:        func() { send(app.Action{Kind: app.ActionQuit}) },
		ViewHistory: func(accountID string) { send(app.Action{Kind: app.ActionViewHistory, AccountID: accountID}) },
	}

	item, err := menubar.Open(state, menubar.BuildMenu(nil, thresholds, sharedActions, providerChoices))
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
	accountItems := menubar.NewAccountItems(sharedActions, thresholds, providerChoices)
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
		recordHistory(histStore, statuses)
		RefreshHistoryWindows(histStore)
		state.Set(menubar.Aggregate(statuses, thresholds))
		item.SetMenu(menubar.BuildMenu(statuses, thresholds, sharedActions, providerChoices))
		// The control item's own icon is redundant once ANY per-account item
		// exists — it shows nothing a per-account item doesn't already (the
		// aggregate severity dot, with no text, versus that item's own dot
		// AND its "NN%/NN%" text) — so it's hidden as soon as there's at
		// least one account, and shown again with none, so there's always
		// SOMETHING to click "Add account…" on. Hidden, not Closed: Close
		// would take the whole platform loop's Hold down with it, which
		// every per-account item's Attach depends on.
		item.SetVisible(len(statuses) == 0)
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
		OnAddAccount: func(provider string) {
			if err := addAccount(store, provider, providerInfo[provider]); err != nil {
				log.Printf("aiquota-tray: adding account: %v", err)
			}
			refresh()
		},
		OnViewHistory: func(accountID string) {
			label := accountID
			if accounts, err := store.List(); err == nil {
				for _, a := range accounts {
					if a.ID == accountID {
						label = a.Label
						break
					}
				}
			}
			if err := openHistoryWindow(histStore, accountID, label); err != nil {
				log.Printf("aiquota-tray: opening history for %s: %v", accountID, err)
			}
		},
	})
	return 0
}

// recordHistory appends each account's session/weekly usage (see
// menubar.ClassifyWindow) to store, once per refresh — the one place
// every poll result already flows through. A write failure is logged,
// matching every other refresh failure path, and does not block the
// rest of the refresh: a menu update or a per-account item is worth
// having even if history couldn't be recorded this time.
func recordHistory(store *history.Store, statuses []menubar.AccountStatus) {
	now := time.Now().Unix()
	for _, s := range statuses {
		if s.Snapshot == nil {
			continue
		}
		for _, w := range s.Snapshot.Windows {
			key, ok := menubar.ClassifyWindow(w.Label)
			if !ok {
				continue
			}
			p := history.Point{AtUnix: now, Used: w.Used, Limit: w.Limit}
			if err := store.Append(s.AccountID, key, p); err != nil {
				log.Printf("aiquota-tray: recording %s history for %s: %v", key, s.AccountID, err)
			}
		}
	}
}

// addAccount opens the onboarding window (see openOnboarding, which picks
// the platform-appropriate mechanism) at info's login URL, waits for it to
// close, and — if the site actually set a cookie — stores the new account
// under provider. A closed window with no cookie (the person cancelled, or
// the login failed) adds nothing; that's not an error worth logging as
// one. info is nil only if provider isn't one discoverProviders found
// (the menu never offers one that isn't, so this is defensive, not an
// expected path).
func addAccount(store *account.Store, provider string, info *quotapb.ProviderInfo) error {
	if info == nil {
		return fmt.Errorf("no provider info for %q (its plugin may have gone away since startup)", provider)
	}
	cookies, err := openOnboarding(info.LoginUrl, info.CookieDomain, onboardWidth, onboardHeight)
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

// discoverProviders finds every installed provider plugin (see
// quota.DiscoverProviders) and calls Describe on each one to learn its
// onboarding details. A plugin that's found but fails to Describe (a
// broken install, a binary that isn't actually a QuotaProvider) is logged
// and skipped rather than failing startup — the same "don't refuse to run
// over one bad provider" stance locateBinary's own doc comment describes.
func discoverProviders(manager *quota.Manager) (map[string]*quotapb.ProviderInfo, []menubar.ProviderChoice) {
	info := map[string]*quotapb.ProviderInfo{}
	var choices []menubar.ProviderChoice
	for _, name := range quota.DiscoverProviders(os.Getenv("PATH")) {
		i, err := manager.Describe(context.Background(), name)
		if err != nil {
			log.Printf("aiquota-tray: describing %s plugin: %v", name, err)
			continue
		}
		info[name] = i
		choices = append(choices, menubar.ProviderChoice{Provider: name, Label: displayName(name)})
	}
	return info, choices
}

// displayName turns a provider id ("claude", "chatgpt") into what the
// "Add account…" picker shows ("Claude", "Chatgpt") — ProviderInfo carries
// no display name of its own (see quotapb.ProviderInfo's own fields), and
// this is a reasonable default without inventing a new contract field for
// a purely cosmetic capitalization a plugin author can't get "wrong".
func displayName(provider string) string {
	if provider == "" {
		return provider
	}
	return strings.ToUpper(provider[:1]) + provider[1:]
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
