// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package capture

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-aiquota/proto/redact"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/quota", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=eyJhbGciOiJIUzI1NiJ9.super-secret-payload.signature-part; Path=/")
		_, _ = w.Write([]byte(`{"used":10,"limit":100,"cookie_echo":"sess_abcdefghijklmnop","account_id":"acct-1234567890"}`))
	})
	mux.HandleFunc("/image", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("not-really-a-png-but-binary-shaped"))
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", maxBodyBytes+100)))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func doGet(t *testing.T, client *http.Client, url string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestTransportRedactsCookieAndAuthorizationHeadersWholesale(t *testing.T) {
	srv := newTestServer(t)
	tr := &Transport{}
	client := &http.Client{Transport: tr}

	resp := doGet(t, client, srv.URL+"/quota", map[string]string{
		"Cookie":        "session=real-session-value-should-never-appear",
		"Authorization": "Bearer real-token-should-never-appear",
	})
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	entries := tr.Entries()
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	e := entries[0]
	if got := e.ReqHeaders["Cookie"]; len(got) != 1 || got[0] != redact.Mask {
		t.Fatalf("Cookie header = %v, want [%q]", got, redact.Mask)
	}
	if got := e.ReqHeaders["Authorization"]; len(got) != 1 || got[0] != redact.Mask {
		t.Fatalf("Authorization header = %v, want [%q]", got, redact.Mask)
	}
	if got := e.RespHeaders["Set-Cookie"]; len(got) != 1 || got[0] != redact.Mask {
		t.Fatalf("response Set-Cookie header = %v, want [%q]", got, redact.Mask)
	}
}

func TestTransportRedactsShapeMatchesInTheBody(t *testing.T) {
	srv := newTestServer(t)
	tr := &Transport{}
	client := &http.Client{Transport: tr}

	resp := doGet(t, client, srv.URL+"/quota", nil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// The real caller must still see the UNREDACTED body: capturing is
	// transparent to whatever it's wrapping.
	if !strings.Contains(string(body), "sess_abcdefghijklmnop") {
		t.Fatalf("real response body was altered by the transport: %s", body)
	}

	entries := tr.Entries()
	e := entries[0]
	if strings.Contains(e.Body, "sess_abcdefghijklmnop") {
		t.Fatalf("captured Entry.Body still contains the raw session-shaped value: %s", e.Body)
	}
	if !strings.Contains(e.Body, redact.Mask) {
		t.Fatalf("captured Entry.Body = %q, want it to contain the redaction mask", e.Body)
	}
	if !strings.Contains(e.Body, `"used":10`) {
		t.Fatalf("captured Entry.Body = %q, want the non-secret JSON shape preserved", e.Body)
	}
}

func TestTransportSkipsNonTextualBodies(t *testing.T) {
	srv := newTestServer(t)
	tr := &Transport{}
	client := &http.Client{Transport: tr}

	resp := doGet(t, client, srv.URL+"/image", nil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "not-really-a-png-but-binary-shaped" {
		t.Fatalf("real image response body was altered: %s", body)
	}

	e := tr.Entries()[0]
	if e.Body != "" {
		t.Fatalf("Entry.Body for a non-textual response = %q, want empty (never buffered)", e.Body)
	}
}

func TestTransportTruncatesLargeBodies(t *testing.T) {
	srv := newTestServer(t)
	tr := &Transport{}
	client := &http.Client{Transport: tr}

	resp := doGet(t, client, srv.URL+"/big", nil)
	full, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if len(full) != maxBodyBytes+100 {
		t.Fatalf("real body len = %d, want %d (untruncated for the real caller)", len(full), maxBodyBytes+100)
	}

	e := tr.Entries()[0]
	if !strings.HasSuffix(e.Body, "…[truncated]") {
		t.Fatalf("captured Entry.Body was not marked truncated: last 30 chars = %q", e.Body[max(0, len(e.Body)-30):])
	}
	if len(e.Body) > maxBodyBytes+len("…[truncated]")+1 {
		t.Fatalf("captured Entry.Body len = %d, want it bounded near maxBodyBytes", len(e.Body))
	}
}

func TestTransportRecordsRoundTripErrors(t *testing.T) {
	tr := &Transport{Base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	client := &http.Client{Transport: tr}

	_, err := client.Get("http://127.0.0.1:0/nope")
	if err == nil {
		t.Fatal("want an error from the round trip")
	}

	e := tr.Entries()[0]
	if e.Err == "" {
		t.Fatal("Entry.Err is empty, want the round-trip error recorded")
	}
	if e.Status != 0 {
		t.Fatalf("Entry.Status = %d, want 0 for a failed round trip", e.Status)
	}
}

func TestTransportOnEntryStreams(t *testing.T) {
	srv := newTestServer(t)
	var streamed []Entry
	tr := &Transport{OnEntry: func(e Entry) { streamed = append(streamed, e) }}
	client := &http.Client{Transport: tr}

	resp := doGet(t, client, srv.URL+"/quota", nil)
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if len(streamed) != 1 {
		t.Fatalf("len(streamed) = %d, want 1", len(streamed))
	}
	if streamed[0].URL != srv.URL+"/quota" {
		t.Fatalf("streamed[0].URL = %q, want %q", streamed[0].URL, srv.URL+"/quota")
	}
}

func TestTransportDefaultsBaseToDefaultTransport(t *testing.T) {
	srv := newTestServer(t)
	tr := &Transport{} // Base left nil
	client := &http.Client{Transport: tr}
	resp := doGet(t, client, srv.URL+"/quota", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (a nil Base must still work via http.DefaultTransport)", resp.StatusCode)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
}

func TestTransportUsesGivenRedactor(t *testing.T) {
	srv := newTestServer(t)
	// A literal doesn't have to look like a credential shape to be one a
	// caller already knows about (e.g. an account id they'd rather not
	// have echoed back in a shared capture file) — New's own literal path
	// is what this exercises, distinct from the built-in shape patterns
	// the other tests cover.
	tr := &Transport{Redactor: redact.New("acct-1234567890")}
	client := &http.Client{Transport: tr}
	resp := doGet(t, client, srv.URL+"/quota", nil)
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	e := tr.Entries()[0]
	if strings.Contains(e.Body, "acct-1234567890") {
		t.Fatalf("Entry.Body = %q, want the custom Redactor's literal masked too", e.Body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
