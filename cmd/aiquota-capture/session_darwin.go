// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build darwin

package main

import (
	"github.com/go-aiquota/proto/redact"
	"github.com/go-aiquota/tray/capture"
)

// captureSession opens a real WKWebView (Apple's own WebKit/Safari engine)
// at loginURL and records every request the page makes via an injected
// script — see capture.RunWebKitCapture's own doc comment for why this is
// the macOS path rather than go-webengine: it's the genuine browser
// engine, so there's no TLS/JS-engine fingerprint gap for a
// Cloudflare-protected site (or any other) to flag in the first place.
func captureSession() ([]capture.Entry, error) {
	return capture.RunWebKitCapture(loginURL, width, height, redact.New())
}
