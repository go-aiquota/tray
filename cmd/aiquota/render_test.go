// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/go-aiquota/proto/quotapb"
)

func renderOf(t *testing.T, f *fake, args ...string) (int, renderDoc, map[string]json.RawMessage) {
	t.Helper()
	code, out, _ := runStatus(t, f, append([]string{"status", "-o", "statusbar", "--render", "1"}, args...)...)
	var d renderDoc
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("not a render description: %v\n%s", err, out)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal([]byte(out), &raw)
	return code, d, raw
}

func TestRender1DescribesEverythingTheShellDraws(t *testing.T) {
	f := one(nil)
	f.ccErr = nil
	f.cc = &quotapb.QuotaSnapshot{PlanKind: "team", Windows: []*quotapb.QuotaWindow{
		win("Session (5hr)", 30, 0), win("Weekly (7 day)", 80, 0), win("Weekly Fable", 99.9, 0)}}
	code, d, raw := renderOf(t, f)
	if code != 0 || d.Render != 1 || len(d.Items) != 3 || d.PollSeconds != defaultPollSeconds || d.Detail == "" {
		t.Fatalf("code %d %+v", code, d)
	}
	for _, k := range []string{"render", "items", "detail", "poll_seconds", "level", "verdict"} {
		if raw[k] == nil {
			t.Errorf("missing key %q", k)
		}
	}
	for i, it := range d.Items {
		if it.ID == "" || it.Text == "" || it.Tooltip == "" || it.Color == "" || it.Alignment != "right" || it.Command != actionDetail {
			t.Errorf("item %d incomplete: %+v", i, it)
		}
		if it.Priority > 0 || it.Priority <= -1 || (i > 0 && it.Priority >= d.Items[i-1].Priority) {
			t.Errorf("item %d priority %v is not strictly decreasing inside (-1, 0]", i, it.Priority)
		}
	}
	if d.Items[1].Background != bgWarning || d.Items[2].Background != bgError {
		t.Errorf("backgrounds: %+v", d.Items)
	}
}

func TestRender1UnknownIsOneGreyItem(t *testing.T) {
	f := one(nil)
	f.ccErr = errors.New("token expired")
	_, d, _ := renderOf(t, f)
	if len(d.Items) != 1 || d.Items[0].Text != "Claude ?" || d.Items[0].Color != unknownColor || d.Verdict != "unknown" {
		t.Errorf("%+v", d)
	}
}

func TestBadRenderIsUsageError(t *testing.T) {
	if code := runArgs(t, "status", "-o", "statusbar", "--render", "2"); code != 2 {
		t.Errorf("--render 2: code %d, want 2", code)
	}
}
