// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aiquota/proto/quotapb"
	"github.com/go-aiquota/tray/account"
	"github.com/go-aiquota/tray/stamp"
)

type fake struct {
	accts []account.Account
	snaps map[string]*quotapb.QuotaSnapshot
	errs  map[string]error
}

func (f *fake) Accounts() ([]account.Account, error) { return f.accts, nil }
func (f *fake) Close()                               {}
func (f *fake) Fetch(_ context.Context, a account.Account) (*quotapb.QuotaSnapshot, error) {
	if e := f.errs[a.Label]; e != nil {
		return nil, e
	}
	return f.snaps[a.Label], nil
}

func win(label string, used float64, reset int64) *quotapb.QuotaWindow {
	return &quotapb.QuotaWindow{Label: label, Used: used, Limit: 100, Unit: "%", ResetsAtUnix: reset}
}

func runStatus(t *testing.T, f *fake, args ...string) (code int, out string, st stamp.Stamp) {
	t.Helper()
	now = func() time.Time { return time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC) }
	newBackend = func() (backend, error) { return f, nil }
	path := filepath.Join(t.TempDir(), "aiquota", "last-status.json")
	var o, e bytes.Buffer
	code = run(append(args, "--stamp", path), &o, &e)
	st, err := stamp.Read(path)
	if err != nil {
		t.Fatalf("no stamp written: %v (stderr %q)", err, e.String())
	}
	return code, o.String(), st
}

func one(snap *quotapb.QuotaSnapshot) *fake {
	return &fake{
		accts: []account.Account{{ID: "a1", Provider: "claude", Label: "max"}},
		snaps: map[string]*quotapb.QuotaSnapshot{"max": snap},
	}
}

func TestAvailable(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{PlanKind: "max", Windows: []*quotapb.QuotaWindow{win("session", 12, 1788000000), win("weekly_all", 40, 1788300000)}})
	code, out, st := runStatus(t, f, "status")
	if code != 0 || st.Verdict != stamp.Available || len(st.Windows) != 2 {
		t.Fatalf("code %d stamp %+v", code, st)
	}
	for _, want := range []string{"plan: max=max", "used 12%", "weekly_all", "AVAILABLE"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestExhaustedWinsOverAvailable(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{win("session", 5, 0), win("weekly_scoped", 100, 1788300000)}})
	code, _, st := runStatus(t, f, "status")
	if code != 1 || st.Verdict != stamp.Exhausted {
		t.Fatalf("code %d verdict %s", code, st.Verdict)
	}
}

func TestFetchErrorIsUnknownNeverAvailable(t *testing.T) {
	f := one(nil)
	f.errs = map[string]error{"max": errors.New("cloudflare challenge")}
	code, out, st := runStatus(t, f, "status")
	if code != 3 || st.Verdict != stamp.Unknown || len(st.Problems) == 0 {
		t.Fatalf("code %d stamp %+v", code, st)
	}
	if !strings.Contains(out, "UNKNOWN") {
		t.Errorf("output: %s", out)
	}
}

func TestNoLimitIsUnknown(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{{Label: "x", Used: 1}}})
	if code, _, st := runStatus(t, f, "status"); code != 3 || st.Verdict != stamp.Unknown {
		t.Fatalf("code %d verdict %s", code, st.Verdict)
	}
}

func TestNoAccountIsUnknown(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{win("session", 1, 0)}})
	if code, _, _ := runStatus(t, f, "status", "--account", "nobody"); code != 3 {
		t.Fatalf("code %d, want 3", code)
	}
}

func TestJSONOutputIsTheStamp(t *testing.T) {
	f := one(&quotapb.QuotaSnapshot{Windows: []*quotapb.QuotaWindow{win("session", 1, 0)}})
	_, out, _ := runStatus(t, f, "status", "-o", "json")
	if !strings.Contains(out, `"verdict": "available"`) {
		t.Errorf("json: %s", out)
	}
}

func TestBadFormatIsUsageError(t *testing.T) {
	f := one(nil)
	newBackend = func() (backend, error) { return f, nil }
	if code := run([]string{"status", "-o", "xml"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("code %d, want 2", code)
	}
}

func TestErrorsAreRedacted(t *testing.T) {
	f := one(nil)
	f.errs = map[string]error{"max": errors.New("boom sk-ant-sid01-abcdefghijklmnopqrstuvwxyz0123456789")}
	_, out, st := runStatus(t, f, "status")
	if strings.Contains(out, "abcdefghijklmnop") || strings.Contains(st.Problems[0], "abcdefghijklmnop") {
		t.Errorf("secret leaked: %s / %v", out, st.Problems)
	}
}
