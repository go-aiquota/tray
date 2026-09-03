// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build darwin

package capture

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-aiquota/proto/redact"
	objc "github.com/go-macos/objc"
)

// RunWebKitCapture opens a REAL WKWebView window (Apple's own WebKit/Safari
// engine, not go-webengine) at loginURL and records every fetch() and
// XMLHttpRequest the page makes, via an injected script — the same
// technique a browser extension uses, not network-level interception.
//
// It exists because go-webengine's own HTTP client, however standards-
// conformant, is still a different TLS/JS-engine implementation than a
// real browser and can be challenged by Cloudflare-protected sites (see
// [Transport]'s sibling capture path in transport.go, built first, which
// hit exactly that wall against claude.ai). WKWebView sidesteps the whole
// question by being the genuine article: there is no fingerprint gap to
// close, because nothing is imitating anything.
//
// It is also, incidentally, MORE private than the Transport path: page JS
// can read fetch()'s response headers but never Set-Cookie, and never sees
// an outgoing Cookie header at all — both are hidden from script by every
// browser's own security model, not by anything this function does. There
// is no cookie value for this capture mechanism to ever redact, because it
// never has the chance to see one.
//
// It blocks until the window is closed and must be called from the
// process's locked main OS thread (see cmd/aiquota-capture's init).
func RunWebKitCapture(loginURL string, width, height int, redactor *redact.Redactor) ([]Entry, error) {
	if err := objc.Load(objc.Foundation, objc.AppKit, objc.WebKit); err != nil {
		return nil, fmt.Errorf("capture: loading AppKit/WebKit: %w", err)
	}
	if redactor == nil {
		redactor = redact.New()
	}

	var entries []Entry
	// Plain RegisterClass, not RegisterClassWithProtocols: unlike
	// WKURLSchemeHandler (which -[WKWebViewConfiguration
	// setURLSchemeHandler:forURLScheme:] genuinely checks via
	// conformsToProtocol:), -[WKUserContentController
	// addScriptMessageHandler:name:] only needs the target to RESPOND TO
	// userContentController:didReceiveScriptMessage: — measured: on this
	// machine objc_getProtocol("WKScriptMessageHandler") returns nil even
	// after WebKit is loaded and a WKWebView class reference is touched
	// first, while WKURLSchemeHandler/WKNavigationDelegate/WKUIDelegate all
	// resolve fine, so the protocol is either not runtime-visible here or
	// not how this particular API checks conformance.
	handlerClass, err := objc.RegisterClass(
		"AiquotaCaptureMessageHandler", objc.GetClass("NSObject"),
		[]objc.MethodDef{
			{
				Cmd: objc.Sel("userContentController:didReceiveScriptMessage:"),
				Fn: func(_self objc.ID, _cmd objc.SEL, _controller objc.ID, message objc.ID) {
					body := message.Send(objc.Sel("body"))
					entries = append(entries, decodeJSEntry(objc.GoString(body), redactor))
				},
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("capture: registering the script message handler class: %w", err)
	}
	handler := objc.ID(handlerClass).Send(objc.Sel("alloc")).Send(objc.Sel("init"))
	handler.Send(objc.Sel("retain"))

	cfg := objc.ClassID("WKWebViewConfiguration").Send(objc.Sel("alloc")).Send(objc.Sel("init"))
	ucc := cfg.Send(objc.Sel("userContentController"))
	ucc.Send(objc.Sel("addScriptMessageHandler:name:"), handler, objc.NSString(messageHandlerName))

	script := objc.ClassID("WKUserScript").Send(objc.Sel("alloc")).Send(
		objc.Sel("initWithSource:injectionTime:forMainFrameOnly:"),
		objc.NSString(captureScript), wkUserScriptInjectionTimeAtDocumentStart, false)
	ucc.Send(objc.Sel("addUserScript:"), script)

	frame := objc.NSRect{Origin: objc.NSPoint{}, Size: objc.NSSize{Width: float64(width), Height: float64(height)}}
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
	win.Send(objc.Sel("setTitle:"), objc.NSString("Log in, browse to your usage/plan page, then close this window"))
	win.Send(objc.Sel("setContentView:"), webView)
	win.Send(objc.Sel("makeKeyAndOrderFront:"), objc.ID(0))

	objc.RunAppLoop(nsApplicationActivationPolicyRegular, func() bool {
		visible := objc.Send[bool](win, objc.Sel("isVisible"))
		return !visible
	})

	return entries, nil
}

// messageHandlerName is what the injected script posts to
// (window.webkit.messageHandlers.<name>.postMessage(...)) and what Go
// registers the handler under; the two must match exactly.
const messageHandlerName = "aiquotaCapture"

// AppKit/WebKit constants used above, spelled out rather than resolved from
// the framework (the same reasoning go-macos/accessibility's
// trustedCheckOptionPrompt uses): they are stable ABI values, and a future
// macOS renaming an exported symbol would be a link error here, not a
// runtime crash from dereferencing a name that moved.
const (
	nsWindowStyleTitled                  = 1 << 0
	nsWindowStyleClosable                = 1 << 1
	nsWindowStyleMiniaturizable          = 1 << 2
	nsWindowStyleResizable               = 1 << 3
	nsBackingStoreBuffered               = 2
	nsApplicationActivationPolicyRegular = 0

	wkUserScriptInjectionTimeAtDocumentStart = 0
)

// jsEntry is what captureScript posts, one per observed request.
type jsEntry struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Err     string            `json:"err"`
	At      string            `json:"at"`
}

// decodeJSEntry turns one posted JSON message into a redacted Entry. A
// message that fails to parse is recorded as an error entry rather than
// silently dropped — a garbled capture is a signal (the page's response
// shape surprised the injected script), not nothing.
func decodeJSEntry(raw string, redactor *redact.Redactor) Entry {
	var j jsEntry
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		return Entry{At: time.Now(), Err: fmt.Sprintf("capture: could not parse a captured message: %v", err)}
	}
	e := Entry{Method: j.Method, URL: j.URL, Status: j.Status, Err: j.Err, Body: redactor.String(j.Body)}
	if t, err := time.Parse(time.RFC3339, j.At); err == nil {
		e.At = t
	} else {
		e.At = time.Now()
	}
	if len(j.Headers) > 0 {
		e.RespHeaders = make(map[string][]string, len(j.Headers))
		for k, v := range j.Headers {
			if sensitiveHeaderNames[strings.ToLower(k)] {
				e.RespHeaders[k] = []string{redact.Mask}
				continue
			}
			e.RespHeaders[k] = []string{redactor.String(v)}
		}
	}
	return e
}

// captureScript is injected at document start, before any page script runs
// (so it wraps the page's OWN fetch/XMLHttpRequest, not some later copy),
// and wraps them the ordinary way a browser extension or a piece of
// analytics code would — nothing here reaches outside what page JS can
// already see. It never touches document.cookie or any request/response
// header a browser hides from script (Cookie, Set-Cookie): those simply
// aren't visible here to capture, redact, or leak.
const captureScript = `
(function() {
  function post(entry) {
    try {
      window.webkit.messageHandlers.` + messageHandlerName + `.postMessage(JSON.stringify(entry));
    } catch (e) {}
  }
  function truncate(s) {
    if (typeof s !== 'string') return '';
    return s.length > 65536 ? s.slice(0, 65536) + '…[truncated]' : s;
  }
  function headersOf(h) {
    var out = {};
    try { h.forEach(function(v, k) { out[k] = v; }); } catch (e) {}
    return out;
  }
  var origFetch = window.fetch;
  if (origFetch) {
    window.fetch = function(input, init) {
      var url = typeof input === 'string' ? input : (input && input.url) || '';
      var method = (init && init.method) || (input && input.method) || 'GET';
      return origFetch.apply(this, arguments).then(function(resp) {
        resp.clone().text().then(function(body) {
          post({ method: method, url: url, status: resp.status, headers: headersOf(resp.headers), body: truncate(body), at: new Date().toISOString() });
        }).catch(function() {
          post({ method: method, url: url, status: resp.status, headers: headersOf(resp.headers), at: new Date().toISOString() });
        });
        return resp;
      }).catch(function(err) {
        post({ method: method, url: url, err: String(err), at: new Date().toISOString() });
        throw err;
      });
    };
  }
  var OrigXHR = window.XMLHttpRequest;
  if (OrigXHR) {
    var origOpen = OrigXHR.prototype.open;
    var origSend = OrigXHR.prototype.send;
    OrigXHR.prototype.open = function(method, url) {
      this.__aiquotaMethod = method;
      this.__aiquotaUrl = url;
      return origOpen.apply(this, arguments);
    };
    OrigXHR.prototype.send = function() {
      var xhr = this;
      xhr.addEventListener('loadend', function() {
        post({ method: xhr.__aiquotaMethod, url: xhr.__aiquotaUrl, status: xhr.status, body: truncate(xhr.responseText || ''), at: new Date().toISOString() });
      });
      return origSend.apply(this, arguments);
    };
  }
})();
`
