// Command aiquota-capture opens an isolated login window and records every
// HTTP request the page makes while it's open, so a person can find which
// request a provider's own web app uses to show usage/quota without their
// real browser's devtools.
//
// On macOS it does this with a real WKWebView (Apple's own Safari/WebKit
// engine): see session_darwin.go for why — go-webengine's own client, however
// standards-conformant, is still a different implementation than a real
// browser and can be challenged by sites behind Cloudflare or similar
// protection, which WKWebView sidesteps by being the genuine article. Every
// other platform falls back to go-webengine (session_other.go), the same
// isolated session go-aiquota/tray's onboarding package uses for adding an
// account, which works fine against sites without that protection.
//
// Log in, click through to wherever your account shows plan/usage, then
// close the window — every entry is redacted (cookies and auth headers
// wholesale, anything credential-shaped elsewhere) before it's held in
// memory or written anywhere, and the file goes outside any git work tree,
// never inside one (see capture.OutDir).
package main

import (
	"fmt"
	"log"
	"os"
	"runtime"

	"github.com/go-aiquota/tray/capture"
)

// init locks the main goroutine to the process's main OS thread before
// anything else runs — opening a native window needs to happen on that
// exact thread; see cmd/aiquota-tray's own init for the incident
// (go-xrkit/desk/cmd/xrdesk) that established this.
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

	entries, err := captureSession()
	if err != nil {
		log.Printf("aiquota-capture: %v", err)
		return 1
	}
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
