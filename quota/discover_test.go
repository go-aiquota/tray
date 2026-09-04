// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package quota

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// touch creates an empty, executable file at dir/name (dir/name.exe on
// Windows, mirroring how DiscoverProviders strips that suffix).
func touch(t *testing.T, dir, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverProvidersFindsEveryPluginBinary(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, BinaryPrefix+"claude")
	touch(t, dir, BinaryPrefix+"chatgpt")
	touch(t, dir, "some-other-tool") // no BinaryPrefix — must be ignored
	touch(t, dir, BinaryPrefix)      // nothing after the prefix — must be ignored

	got := DiscoverProviders(dir)
	want := []string{"chatgpt", "claude"} // sorted
	if len(got) != len(want) {
		t.Fatalf("DiscoverProviders() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DiscoverProviders() = %v, want %v", got, want)
		}
	}
}

func TestDiscoverProvidersDedupesAcrossPathEntries(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	touch(t, dir1, BinaryPrefix+"claude")
	touch(t, dir2, BinaryPrefix+"claude")
	touch(t, dir2, BinaryPrefix+"chatgpt")

	path := dir1 + string(os.PathListSeparator) + dir2
	got := DiscoverProviders(path)
	if len(got) != 2 || got[0] != "chatgpt" || got[1] != "claude" {
		t.Fatalf("DiscoverProviders() = %v, want [chatgpt claude] with claude deduped", got)
	}
}

func TestDiscoverProvidersSkipsAMissingOrUnreadableDir(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, BinaryPrefix+"claude")

	path := filepath.Join(dir, "does-not-exist") + string(os.PathListSeparator) + dir
	got := DiscoverProviders(path)
	if len(got) != 1 || got[0] != "claude" {
		t.Fatalf("DiscoverProviders() = %v, want [claude]", got)
	}
}

func TestDiscoverProvidersEmptyPathFindsNothing(t *testing.T) {
	if got := DiscoverProviders(""); len(got) != 0 {
		t.Fatalf("DiscoverProviders(\"\") = %v, want none", got)
	}
}

func TestDiscoverProvidersIgnoresDirectoriesNamedLikeAPlugin(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, BinaryPrefix+"claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DiscoverProviders(dir); len(got) != 0 {
		t.Fatalf("DiscoverProviders() = %v, want none (a directory is not a plugin binary)", got)
	}
}
