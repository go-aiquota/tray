// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build darwin

package main

import "github.com/go-aiquota/tray/onboarding"

// openOnboarding drives an isolated login for a provider at loginURL using
// a real WKWebView (see onboarding.RunWebKitLogin's own doc comment for
// why: claude.ai's Cloudflare protection challenges go-webengine's client
// at the login page itself, a wall a genuine WebKit session doesn't hit).
// The account's organization UUID, when the login response revealed one,
// is folded into the returned map under orgUUIDCredentialKey — the one
// non-cookie field plugin-claude's FetchQuota needs alongside the cookies.
func openOnboarding(loginURL, cookieDomain string, w, h int) (map[string]string, error) {
	cookies, orgUUID, err := onboarding.RunWebKitLogin(loginURL, cookieDomain, w, h)
	if err != nil {
		return nil, err
	}
	if orgUUID != "" {
		cookies[orgUUIDCredentialKey] = orgUUID
	}
	return cookies, nil
}
