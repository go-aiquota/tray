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
	"github.com/go-aiquota/tray/onboarding"
	"github.com/go-aiquota/tray/quota"
	"github.com/go-widgets/application"
	"github.com/go-widgets/mvvm"
	"github.com/go-widgets/tray"
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
	// cookieDomain is what onboarding.Window.Cookies is asked for after a
	// login: the WHOLE cookie jar for the domain, not one named cookie —
	// this program does not know which cookie claude.ai actually uses for
	// a session, and guessing its name would be worse than storing a few
	// extra harmless ones. The plugin that reads them back decides which
	// it needs.
	cookieDomain = "claude.ai"

	pollInterval  = 5 * time.Minute
	pollTimeout   = 20 * time.Second
	onboardWidth  = 480
	onboardHeight = 640
)

func main() { os.Exit(run()) }

// menuAction is what a click on a non-account tray row means. It travels
// through a channel rather than running its OnClick body directly: OnClick
// fires from the backend's own event handling, off the locked main thread
// that (*menubar.Tray).Hold occupies, and both opening a window and
// stopping Hold's loop must happen from the goroutine that called Hold —
// see (*Tray).Hold's own doc comment.
type menuAction int

const (
	actionNone menuAction = iota
	actionAddAccount
	actionLockNow
	actionQuit
)

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

	actions := make(chan menuAction, 4)
	send := func(a menuAction) {
		select {
		case actions <- a:
		default:
			log.Printf("aiquota-tray: %v was chosen while the tray was not listening; dropped", a)
		}
	}

	thresholds := menubar.DefaultThresholds
	state := mvvm.NewObservable(menubar.SeverityUnknown)
	var current []menubar.AccountStatus

	buildMenu := func() *tray.Menu {
		return menubar.BuildMenu(current, thresholds, menubar.Actions{
			AddAccount: func() { send(actionAddAccount) },
			LockNow:    func() { send(actionLockNow) },
			Quit:       func() { send(actionQuit) },
		})
	}

	item, err := menubar.Open(state, buildMenu())
	if err != nil {
		log.Printf("aiquota-tray: %v", err)
		return 1
	}
	defer func() { _ = item.Close() }()

	refresh := func() {
		ctx, cancel := context.WithTimeout(context.Background(), pollInterval)
		defer cancel()
		statuses, err := app.PollAccounts(ctx, store, manager, cache, pollTimeout)
		if err != nil {
			// Kept: current stays whatever it last was, rather than being
			// wiped by a transient accounts.json read hiccup.
			log.Printf("aiquota-tray: polling accounts: %v", err)
			return
		}
		current = statuses
		state.Set(menubar.Aggregate(current, thresholds))
		item.SetMenu(buildMenu())
	}
	refresh()

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

	// The tray owns the platform run loop while no window is open — Hold
	// blocks on it — and gives it up (Release) only to open the onboarding
	// window, which then drives its own loop for as long as it's up. See
	// (*menubar.Tray)'s doc comment and go-xrkit/desk/traydesk.go, the
	// precedent this mirrors.
	for {
		got := actionNone
		done := make(chan struct{})
		go func() {
			defer close(done)
			got = <-actions
			item.Release()
		}()
		if err := item.Hold(); err != nil {
			log.Printf("aiquota-tray: tray: %v", err)
		}
		<-done

		switch got {
		case actionQuit:
			return 0
		case actionLockNow:
			cache.Lock()
		case actionAddAccount:
			if err := addAccount(store); err != nil {
				log.Printf("aiquota-tray: adding account: %v", err)
			}
			refresh()
		}
	}
}

// addAccount opens the onboarding window, waits for it to close, and — if
// the site actually set a cookie — stores the new account. A closed window
// with no cookie (the person cancelled, or the login failed) adds nothing;
// that's not an error worth logging as one.
func addAccount(store *account.Store) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	win, err := onboarding.Open(ctx, loginURL, onboardWidth, onboardHeight)
	if err != nil {
		return fmt.Errorf("opening the login window: %w", err)
	}
	defer win.Close()

	spec := application.Spec{Name: "AI Quota", Identifier: "com.go-aiquota.tray", Version: "0.1.0"}
	cfg := application.Config{Title: "Sign in", Width: onboardWidth, Height: onboardHeight}
	if err := application.Run(spec, cfg, win, nil); err != nil {
		return fmt.Errorf("running the login window: %w", err)
	}

	cookies := win.Cookies(cookieDomain)
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
