// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build darwin

package capture

import (
	"strings"
	"testing"

	"github.com/go-aiquota/proto/redact"
)

func TestDecodeJSEntryValidMessage(t *testing.T) {
	raw := `{"method":"GET","url":"https://claude.ai/api/usage","status":200,"headers":{"content-type":"application/json"},"body":"{\"used\":10,\"limit\":100}","at":"2026-09-03T12:00:00Z"}`
	e := decodeJSEntry(raw, redact.New())
	if e.Method != "GET" || e.URL != "https://claude.ai/api/usage" || e.Status != 200 {
		t.Fatalf("decoded entry = %+v, want method/url/status from the message", e)
	}
	if e.Body != `{"used":10,"limit":100}` {
		t.Fatalf("Body = %q, want the JSON body unaltered (nothing secret-shaped in it)", e.Body)
	}
	if got := e.RespHeaders["content-type"]; len(got) != 1 || got[0] != "application/json" {
		t.Fatalf("RespHeaders[content-type] = %v, want [application/json]", got)
	}
	if e.At.IsZero() {
		t.Fatal("At was not parsed from the message's timestamp")
	}
}

func TestDecodeJSEntryRedactsBodyAndHeaders(t *testing.T) {
	raw := `{"method":"GET","url":"https://claude.ai/api/usage","status":200,` +
		`"headers":{"set-cookie":"session=abc","x-custom":"sess_abcdefghijklmnop"},` +
		`"body":"token: sess_abcdefghijklmnop","at":"2026-09-03T12:00:00Z"}`
	e := decodeJSEntry(raw, redact.New())
	if strings.Contains(e.Body, "sess_abcdefghijklmnop") {
		t.Fatalf("Body = %q, want the session-shaped value redacted", e.Body)
	}
	if got := e.RespHeaders["set-cookie"]; len(got) != 1 || got[0] != redact.Mask {
		t.Fatalf("set-cookie header = %v, want wholesale redaction by name", got)
	}
	if got := e.RespHeaders["x-custom"]; len(got) != 1 || got[0] != redact.Mask {
		t.Fatalf("x-custom header = %v, want the session-shaped value redacted", got)
	}
}

func TestDecodeJSEntryMalformedJSONBecomesAnErrorEntry(t *testing.T) {
	e := decodeJSEntry("not json at all", redact.New())
	if e.Err == "" {
		t.Fatal("Err is empty, want a parse-failure message for malformed JSON")
	}
	if e.At.IsZero() {
		t.Fatal("At should fall back to now() when the message has no valid timestamp")
	}
}

func TestDecodeJSEntryCarriesAnErrField(t *testing.T) {
	raw := `{"method":"GET","url":"https://claude.ai/x","err":"network error","at":"2026-09-03T12:00:00Z"}`
	e := decodeJSEntry(raw, redact.New())
	if e.Err != "network error" {
		t.Fatalf("Err = %q, want %q", e.Err, "network error")
	}
	if e.Status != 0 {
		t.Fatalf("Status = %d, want 0 for a request that never got a response", e.Status)
	}
}

func TestDecodeJSEntryInvalidTimestampFallsBackToNow(t *testing.T) {
	raw := `{"method":"GET","url":"https://claude.ai/x","status":200,"at":"not-a-timestamp"}`
	e := decodeJSEntry(raw, redact.New())
	if e.At.IsZero() {
		t.Fatal("At should fall back to now() when the timestamp doesn't parse")
	}
}

func TestDecodeJSEntryNoHeadersLeavesRespHeadersNil(t *testing.T) {
	raw := `{"method":"GET","url":"https://claude.ai/x","status":200,"at":"2026-09-03T12:00:00Z"}`
	e := decodeJSEntry(raw, redact.New())
	if e.RespHeaders != nil {
		t.Fatalf("RespHeaders = %v, want nil when the message carried none", e.RespHeaders)
	}
}
