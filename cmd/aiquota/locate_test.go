// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func pluginFile(t *testing.T, dir string) string {
	t.Helper()
	name := "go-aiquota-plugin-claude"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func withExecutable(t *testing.T, exe string) {
	t.Helper()
	old := executable
	executable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { executable = old })
}

// A plugin unpacked beside aiquota is found although its folder is not on PATH.
func TestPluginBesideTheExecutableIsFoundOffPath(t *testing.T) {
	dir := t.TempDir()
	want := pluginFile(t, dir)
	withExecutable(t, filepath.Join(dir, "aiquota"))
	t.Setenv("PATH", t.TempDir())
	got, err := locateBinary("claude")
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestPathWinsOverTheExecutableDirectory(t *testing.T) {
	onPath, beside := t.TempDir(), t.TempDir()
	want := pluginFile(t, onPath)
	pluginFile(t, beside)
	withExecutable(t, filepath.Join(beside, "aiquota"))
	t.Setenv("PATH", onPath)
	got, err := locateBinary("claude")
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestMissingPluginSaysWhereItWasLookedFor(t *testing.T) {
	withExecutable(t, filepath.Join(t.TempDir(), "aiquota"))
	t.Setenv("PATH", t.TempDir())
	_, err := locateBinary("claude")
	if err == nil || !strings.Contains(err.Error(), "go-aiquota-plugin-claude on PATH or next to aiquota") {
		t.Fatalf("error %v", err)
	}
}
