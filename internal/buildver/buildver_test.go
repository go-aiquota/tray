// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package buildver

import (
	"runtime/debug"
	"testing"
)

func TestLinkTimeVersionWins(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "v1.2.3"
	if got := Version(); got != "v1.2.3" {
		t.Fatalf("got %q", got)
	}
	version = ""
	if Version() == "" {
		t.Fatal("empty version")
	}
}

func TestVersionFallbacks(t *testing.T) {
	oldV, oldR := version, readBuildInfo
	t.Cleanup(func() { version, readBuildInfo = oldV, oldR })
	version = ""
	cases := []struct {
		name string
		bi   *debug.BuildInfo
		ok   bool
		want string
	}{
		{"module version recorded by go install", &debug.BuildInfo{Main: debug.Module{Version: "v0.8.2"}}, true, "v0.8.2"},
		{"build info without a version", &debug.BuildInfo{}, true, "(devel)"},
		{"no build info at all", nil, false, "(devel)"},
	}
	for _, c := range cases {
		readBuildInfo = func() (*debug.BuildInfo, bool) { return c.bi, c.ok }
		if got := Version(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	// A link-time version beats the recorded one.
	version = "v9.9.9"
	readBuildInfo = func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: "v0.8.2"}}, true }
	if got := Version(); got != "v9.9.9" {
		t.Errorf("got %q", got)
	}
}
