// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"strings"
	"testing"
)

// An error that ends in exit 2 says what it was: cobra's own printing is
// silenced, and a silent exit 2 left a user (and a CI log) with nothing to act on.
func TestUsageErrorIsPrinted(t *testing.T) {
	var o, e bytes.Buffer
	if code := run([]string{"status", "-o", "xml"}, &o, &e); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(e.String(), "aiquota: -o: want text, json or statusbar") {
		t.Fatalf("stderr %q", e.String())
	}
}
