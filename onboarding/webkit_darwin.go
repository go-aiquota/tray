// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build darwin

package onboarding

import (
	"fmt"
	"strings"
	"time"

	objc "github.com/go-macos/objc"
)

// RunWebKitLogin opens a REAL, ISOLATED WKWebView window (Apple's own
// WebKit/Safari engine) at loginURL, blocks until the person closes it, and
// returns whatever session cookies and organization UUID the login left
// behind.
//
// It exists for the same reason capture.RunWebKitCapture does (see that
// package's own doc comment): go-webengine's client, however standards-
// conformant, is a different TLS/JS-engine implementation than a real
// browser and gets challenged by claude.ai's Cloudflare protection at the
// login page itself — a wall a genuine WebKit session simply doesn't hit,
// because nothing about it needs to be told apart from any other browser.
//
// Isolation: the WKWebViewConfiguration is built on
// WKWebsiteDataStore.nonPersistentDataStore — an in-memory store discarded
// when this function returns — never the shared default store capture.go
// uses for its own, deliberately-run-by-hand debugging purpose. A real
// account onboarding must not leave session state sitting in a store any
// other WKWebView-based process on the machine could read, and must not
// let one account's login see a previous one's cookies either.
//
// org_uuid discovery: claude.ai's own web app resolves which organization
// a session belongs to from a bootstrap call that (as far as this project
// has confirmed) only returns it once client-side state already names an
// org — there is no independently-callable "which org am I in" endpoint
// reachable with just a fresh cookie jar. The one place the UUID has been
// observed appearing is the login response itself
// (account.memberships[].organization.uuid), so an injected script watches
// every fetch() response during the session for exactly that shape and
// reports the first UUID it finds — see captureOrgUUIDScript.
func RunWebKitLogin(loginURL, cookieDomain string, w, h int) (cookies map[string]string, orgUUID string, err error) {
	if err := objc.Load(objc.Foundation, objc.AppKit, objc.WebKit); err != nil {
		return nil, "", fmt.Errorf("onboarding: loading AppKit/WebKit: %w", err)
	}

	var foundOrgUUID string
	handlerClass, err := objc.RegisterClass(
		"AiquotaOrgUUIDHandler", objc.GetClass("NSObject"),
		[]objc.MethodDef{
			{
				Cmd: objc.Sel("userContentController:didReceiveScriptMessage:"),
				Fn: func(_self objc.ID, _cmd objc.SEL, _controller objc.ID, message objc.ID) {
					if foundOrgUUID == "" {
						foundOrgUUID = objc.GoString(message.Send(objc.Sel("body")))
					}
				},
			},
		},
	)
	if err != nil {
		return nil, "", fmt.Errorf("onboarding: registering the script message handler class: %w", err)
	}
	handler := objc.ID(handlerClass).Send(objc.Sel("alloc")).Send(objc.Sel("init"))
	handler.Send(objc.Sel("retain"))

	dataStore := objc.ClassID("WKWebsiteDataStore").Send(objc.Sel("nonPersistentDataStore"))
	dataStore.Send(objc.Sel("retain"))

	cfg := objc.ClassID("WKWebViewConfiguration").Send(objc.Sel("alloc")).Send(objc.Sel("init"))
	cfg.Send(objc.Sel("setWebsiteDataStore:"), dataStore)
	ucc := cfg.Send(objc.Sel("userContentController"))
	ucc.Send(objc.Sel("addScriptMessageHandler:name:"), handler, objc.NSString(orgUUIDHandlerName))

	script := objc.ClassID("WKUserScript").Send(objc.Sel("alloc")).Send(
		objc.Sel("initWithSource:injectionTime:forMainFrameOnly:"),
		objc.NSString(captureOrgUUIDScript), wkUserScriptInjectionTimeAtDocumentStart, false)
	ucc.Send(objc.Sel("addUserScript:"), script)

	frame := objc.NSRect{Origin: objc.NSPoint{}, Size: objc.NSSize{Width: float64(w), Height: float64(h)}}
	webView := objc.ClassID("WKWebView").Send(objc.Sel("alloc")).Send(
		objc.Sel("initWithFrame:configuration:"), frame, cfg)
	webView.Send(objc.Sel("retain"))

	nsURL := objc.ClassID("NSURL").Send(objc.Sel("URLWithString:"), objc.NSString(loginURL))
	req := objc.ClassID("NSURLRequest").Send(objc.Sel("requestWithURL:"), nsURL)
	webView.Send(objc.Sel("loadRequest:"), req)

	style := nsWindowStyleTitled | nsWindowStyleClosable | nsWindowStyleMiniaturizable | nsWindowStyleResizable
	win := objc.ClassID("NSWindow").Send(objc.Sel("alloc")).Send(
		objc.Sel("initWithContentRect:styleMask:backing:defer:"), frame, style, nsBackingStoreBuffered, false)
	win.Send(objc.Sel("retain"))
	win.Send(objc.Sel("setTitle:"), objc.NSString("Log in to add this account, then close this window"))
	win.Send(objc.Sel("setContentView:"), webView)
	win.Send(objc.Sel("makeKeyAndOrderFront:"), objc.ID(0))

	cookiesCh := make(chan map[string]string, 1)
	var fetchTriggered bool
	var closedAt time.Time
	const cookieFetchTimeout = 5 * time.Second

	objc.RunAppLoop(nsApplicationActivationPolicyRegular, func() bool {
		if objc.Send[bool](win, objc.Sel("isVisible")) {
			return false
		}
		if !fetchTriggered {
			fetchTriggered = true
			closedAt = time.Now()
			triggerCookieFetch(dataStore, cookieDomain, cookiesCh)
		}
		select {
		case cookies = <-cookiesCh:
			return true
		default:
			// getAllCookies: is asynchronous and needs THIS SAME run loop
			// to keep pumping for its completion block to be dispatched —
			// blocking here to wait for it would deadlock the very loop
			// that has to service it. Returning false just keeps RunAppLoop
			// going for another ~50ms tick, which is exactly what lets the
			// completion arrive on a later call to this same function.
			return time.Since(closedAt) > cookieFetchTimeout
		}
	})

	if cookies == nil {
		cookies = map[string]string{}
	}
	return cookies, foundOrgUUID, nil
}

// orgUUIDHandlerName is the JS message-handler name captureOrgUUIDScript
// posts to; the two must match exactly.
const orgUUIDHandlerName = "aiquotaOrgUUID"

// captureOrgUUIDScript wraps window.fetch (the same technique
// capture.RunWebKitCapture uses, injected at document start so it wraps
// the page's OWN fetch, not a later copy) and, for every JSON response
// whose body is shaped like { account: { memberships: [{ organization:
// { uuid } }] } } — the shape actually observed in claude.ai's own login
// response — posts the first uuid it finds. It never inspects a request,
// only response bodies already visible to the page's own JS.
const captureOrgUUIDScript = `
(function() {
  function tryReport(bodyText) {
    try {
      var data = JSON.parse(bodyText);
      var acct = data && data.account;
      var memberships = acct && acct.memberships;
      if (Array.isArray(memberships)) {
        for (var i = 0; i < memberships.length; i++) {
          var org = memberships[i] && memberships[i].organization;
          if (org && org.uuid) {
            window.webkit.messageHandlers.` + orgUUIDHandlerName + `.postMessage(org.uuid);
            return;
          }
        }
      }
    } catch (e) {}
  }
  var origFetch = window.fetch;
  if (origFetch) {
    window.fetch = function() {
      return origFetch.apply(this, arguments).then(function(resp) {
        resp.clone().text().then(tryReport).catch(function() {});
        return resp;
      });
    };
  }
})();
`

// triggerCookieFetch asynchronously reads every cookie out of dataStore
// and sends it (as name->value, filtered to cookieDomain) to out — never
// blocking the caller, since the completion only ever arrives via the
// SAME run loop the caller must keep pumping (see RunWebKitLogin).
func triggerCookieFetch(dataStore objc.ID, cookieDomain string, out chan<- map[string]string) {
	store := dataStore.Send(objc.Sel("httpCookieStore"))
	block := objc.NewBlock(func(b objc.Block, cookiesArray objc.ID) {
		defer b.Release()
		result := map[string]string{}
		count := objc.Send[int](cookiesArray, objc.Sel("count"))
		for i := 0; i < count; i++ {
			cookie := cookiesArray.Send(objc.Sel("objectAtIndex:"), i)
			domain := objc.GoString(cookie.Send(objc.Sel("domain")))
			if !strings.HasSuffix(domain, cookieDomain) {
				continue
			}
			name := objc.GoString(cookie.Send(objc.Sel("name")))
			value := objc.GoString(cookie.Send(objc.Sel("value")))
			result[name] = value
		}
		select {
		case out <- result:
		default:
		}
	})
	store.Send(objc.Sel("getAllCookies:"), block)
}

// AppKit/WebKit constants, spelled out rather than resolved from the
// framework — see capture.webkit_darwin.go's identical constants for why
// (the same reasoning go-macos/accessibility's trustedCheckOptionPrompt
// uses). Deliberately duplicated rather than imported from the capture
// package: onboarding must not depend on a debugging tool, and the values
// are small, stable ABI constants, not logic worth sharing.
const (
	nsWindowStyleTitled                  = 1 << 0
	nsWindowStyleClosable                = 1 << 1
	nsWindowStyleMiniaturizable          = 1 << 2
	nsWindowStyleResizable               = 1 << 3
	nsBackingStoreBuffered               = 2
	nsApplicationActivationPolicyRegular = 0

	wkUserScriptInjectionTimeAtDocumentStart = 0
)
