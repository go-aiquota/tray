// Command aiquota-capture opens an isolated login window — the same kind
// go-aiquota/tray's onboarding package uses for adding an account — and
// records every HTTP request the page makes while it's open: the initial
// navigation AND every JS-initiated fetch()/XMLHttpRequest, since
// go-webengine/engine's Client is one shared *http.Client for both (see
// the capture package's own doc comment).
//
// It exists to answer one question without needing a real browser's
// devtools: which request does claude.ai's own web app make to show usage
// quota, and what does its response look like? Log in, click through to
// wherever your account shows plan/usage, then close the window — every
// entry is redacted (cookies and auth headers wholesale, anything
// credential-shaped elsewhere) before it's held in memory or written
// anywhere, and the file is written outside any git work tree, never
// inside one (see capture.OutDir).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/go-aiquota/tray/capture"
	"github.com/go-aiquota/tray/onboarding"
	"github.com/go-webengine/browserproxy"
	"github.com/go-webengine/engine"
	"github.com/go-widgets/application"
)

// init locks the main goroutine to the process's main OS thread before
// anything else runs — the onboarding window needs to create a native
// object (NSWindow) on that exact thread; see cmd/aiquota-tray's own init
// for the incident (go-xrkit/desk/cmd/xrdesk) that established this.
func init() { runtime.LockOSThread() }

const (
	loginURL = "https://claude.ai/login"
	width    = 480
	height   = 720
)

func main() { os.Exit(run()) }

func run() int {
	dir, err := capture.OutDir(os.Getenv(capture.OutDirEnv))
	if err != nil {
		log.Printf("aiquota-capture: %v", err)
		return 1
	}

	eng := engine.New()
	guarded := browserproxy.GuardedClient(eng.Client)
	tr := &capture.Transport{Base: guarded.Transport}
	guarded.Transport = tr
	eng.Client = guarded

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	win, err := onboarding.OpenWith(ctx, eng, loginURL, width, height)
	if err != nil {
		log.Printf("aiquota-capture: opening the window: %v", err)
		return 1
	}
	defer win.Close()

	spec := application.Spec{Name: "AI Quota Capture", Identifier: "com.go-aiquota.capture", Version: "0.1.0"}
	cfg := application.Config{
		Title:  "Log in, browse to your usage/plan page, then close this window",
		Width:  width,
		Height: height,
	}
	if err := application.Run(spec, cfg, win, nil); err != nil {
		log.Printf("aiquota-capture: running the window: %v", err)
		return 1
	}

	entries := tr.Entries()
	if len(entries) == 0 {
		fmt.Println("no requests were observed")
		return 0
	}
	path, err := capture.WriteEntries(dir, entries)
	if err != nil {
		log.Printf("aiquota-capture: writing the capture: %v", err)
		return 1
	}
	fmt.Printf("%d requests captured, written to %s\n", len(entries), path)
	fmt.Println("look through it for a JSON response whose body mentions usage, limit, quota, or reset")
	return 0
}
