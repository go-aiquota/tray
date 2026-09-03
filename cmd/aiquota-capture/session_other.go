// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !darwin

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/go-aiquota/tray/capture"
	"github.com/go-aiquota/tray/onboarding"
	"github.com/go-webengine/browserproxy"
	"github.com/go-webengine/engine"
	"github.com/go-widgets/application"
)

// captureSession opens go-webengine's own isolated session — the same
// mechanism go-aiquota/tray's onboarding package uses for adding an
// account — and records every HTTP request it makes: the initial
// navigation AND every JS-initiated fetch()/XMLHttpRequest, since
// engine.Engine.Client is one shared *http.Client for both. This is the
// fallback for platforms without a native WKWebView-equivalent binding;
// it works fine against sites with no Cloudflare-style protection.
func captureSession() ([]capture.Entry, error) {
	eng := engine.New()
	guarded := browserproxy.GuardedClient(eng.Client)
	tr := &capture.Transport{Base: guarded.Transport}
	guarded.Transport = tr
	eng.Client = guarded

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	win, err := onboarding.OpenWith(ctx, eng, loginURL, width, height)
	if err != nil {
		return nil, fmt.Errorf("opening the window: %w", err)
	}
	defer win.Close()

	spec := application.Spec{Name: "AI Quota Capture", Identifier: "com.go-aiquota.capture", Version: "0.1.0"}
	cfg := application.Config{
		Title:  "Log in, browse to your usage/plan page, then close this window",
		Width:  width,
		Height: height,
	}
	if err := application.Run(spec, cfg, win, nil); err != nil {
		return nil, fmt.Errorf("running the window: %w", err)
	}
	return tr.Entries(), nil
}
