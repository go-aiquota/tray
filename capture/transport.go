// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package capture records the HTTP traffic an onboarding.Window's engine
// makes during a real, isolated login session — so a person can find which
// request a provider's own web app uses to show usage/quota, without
// opening their real browser's devtools. It exists because
// go-webengine/engine already makes this possible with no changes of its
// own: engine.Engine.Client is a plain, exported *http.Client, and the
// SAME client backs both the initial page load and every JS-initiated
// fetch()/XMLHttpRequest for the life of a LiveDocument (dynamic.go's
// jsOptions passes the identical pointer into the JS runtime) — so
// wrapping Client.Transport before opening a Window observes everything.
//
// Every entry this package produces has already had credential-shaped and
// credential-carrying values replaced by redact.Mask before it is ever
// held in memory or written anywhere — see Transport's own doc comment.
package capture

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-aiquota/proto/redact"
)

// sensitiveHeaderNames are redacted wholesale, by NAME, regardless of
// whether their value happens to match redact's shape patterns: a cookie
// or bearer token does not have to look like a JWT or a sess_-prefixed
// string to be one. A header either carries credentials or it doesn't;
// there is no partial case worth preserving.
var sensitiveHeaderNames = map[string]bool{
	"cookie":              true,
	"set-cookie":          true,
	"authorization":       true,
	"x-csrf-token":        true,
	"x-xsrf-token":        true,
	"proxy-authorization": true,
}

// maxBodyBytes bounds how much of a response body is STORED (redacted text
// beyond this is dropped from the capture, never from what the real
// caller — the page's own fetch, or the engine's navigation — receives).
// A capture exists to learn a request's shape, not to mirror an account's
// entire usage history: unbounded capture of a real account's real
// traffic is its own privacy and size risk, worth bounding by default.
const maxBodyBytes = 64 * 1024

// Entry is one observed request/response, fully redacted.
type Entry struct {
	At          time.Time
	Method      string
	URL         string
	ReqHeaders  map[string][]string
	Status      int
	RespHeaders map[string][]string
	// Body is the redacted, possibly-truncated response text — empty for a
	// non-textual Content-Type (never buffered in the first place) or a
	// failed round trip.
	Body string
	// Err is the round-trip error, if the request never got a response.
	Err string
}

// Transport wraps a base http.RoundTripper (http.DefaultTransport if nil)
// and records every request/response that passes through it as a redacted
// Entry — in memory (see Entries) and, if OnEntry is set, streamed to it
// as each one completes, so a caller can persist them incrementally rather
// than losing a whole session if the process is interrupted partway
// through it.
//
// It is transparent to whatever sits on top of it: every response body is
// restored in full before being handed back, so wrapping this in requires
// no other change to an engine.Engine or its caller.
type Transport struct {
	Base     http.RoundTripper
	Redactor *redact.Redactor
	OnEntry  func(Entry)

	mu      sync.Mutex
	entries []Entry
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	entry := Entry{
		At:         time.Now(),
		Method:     req.Method,
		URL:        req.URL.String(),
		ReqHeaders: t.redactHeaders(req.Header),
	}
	resp, err := base.RoundTrip(req)
	if err != nil {
		entry.Err = err.Error()
		t.record(entry)
		return resp, err
	}
	entry.Status = resp.StatusCode
	entry.RespHeaders = t.redactHeaders(resp.Header)
	entry.Body = t.readBody(resp)
	t.record(entry)
	return resp, err
}

// Entries returns every request observed so far, in the order they
// completed.
func (t *Transport) Entries() []Entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Entry(nil), t.entries...)
}

func (t *Transport) record(e Entry) {
	t.mu.Lock()
	t.entries = append(t.entries, e)
	t.mu.Unlock()
	if t.OnEntry != nil {
		t.OnEntry(e)
	}
}

func (t *Transport) redactor() *redact.Redactor {
	if t.Redactor != nil {
		return t.Redactor
	}
	return redact.New()
}

func (t *Transport) redactHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for name, values := range h {
		if sensitiveHeaderNames[strings.ToLower(name)] {
			out[name] = []string{redact.Mask}
			continue
		}
		red := make([]string, len(values))
		for i, v := range values {
			red[i] = t.redactor().String(v)
		}
		out[name] = red
	}
	return out
}

// readBody restores resp.Body's FULL, unaltered content for the real
// caller (the engine's navigation, or the page's own fetch/XHR) — capturing
// must never shortchange the session it's observing — and returns a
// redacted, size-bounded copy for the Entry. A non-textual response
// (an image, a font, a media file) is left entirely unread and unbuffered:
// nothing this tool is looking for lives there, and buffering it would
// only cost memory.
func (t *Transport) readBody(resp *http.Response) string {
	if resp.Body == nil || !looksTextual(resp.Header.Get("Content-Type")) {
		return ""
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	stored := data
	truncated := false
	if len(stored) > maxBodyBytes {
		stored = stored[:maxBodyBytes]
		truncated = true
	}
	text := t.redactor().String(string(stored))
	if truncated {
		text += "…[truncated]"
	}
	return text
}

func looksTextual(contentType string) bool {
	ct := strings.ToLower(contentType)
	if ct == "" {
		// No declared type at all is unusual for a real response but not
		// disqualifying — read it rather than silently skip a candidate.
		return true
	}
	for _, want := range []string{"json", "text/", "javascript", "xml"} {
		if strings.Contains(ct, want) {
			return true
		}
	}
	return false
}
