// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package buildver

import "testing"

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
