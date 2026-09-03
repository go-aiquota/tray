// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !darwin

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/go-aiquota/tray/onboarding"
	"github.com/go-widgets/application"
)

// openOnboarding drives an isolated login via go-webengine — the only
// mechanism available on this platform. NOTE: claude.ai specifically is
// NOT reachable this way — its login page is behind a Cloudflare
// challenge go-webengine's client cannot pass (see
// onboarding.RunWebKitLogin's own doc comment, darwin-only). This path
// still works for a future provider without that protection; a non-macOS
// real-browser-engine onboarding path for Claude itself doesn't exist yet.
func openOnboarding(loginURL, cookieDomain string, w, h int) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	win, err := onboarding.Open(ctx, loginURL, w, h)
	if err != nil {
		return nil, fmt.Errorf("opening the login window: %w", err)
	}
	defer win.Close()

	spec := application.Spec{Name: "AI Quota", Identifier: "com.go-aiquota.tray", Version: "0.1.0"}
	cfg := application.Config{Title: "Sign in", Width: float64(w), Height: float64(h)}
	if err := application.Run(spec, cfg, win, nil); err != nil {
		return nil, fmt.Errorf("running the login window: %w", err)
	}
	return win.Cookies(cookieDomain), nil
}
