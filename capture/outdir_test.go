// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoRootOfFindsThisRepository(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if root := repoRootOf(wd); root == "" {
		t.Fatalf("repoRootOf(%q) found no work tree, and this file is in one", wd)
	}
	// Deep inside it too — the walk must not stop at the first parent.
	if root := repoRootOf(filepath.Join(wd, "testdata", "artifacts")); root == "" {
		t.Error("repoRootOf did not walk up out of testdata/artifacts")
	}
}

func TestRepoRootOfAcceptsOutsideAnyRepo(t *testing.T) {
	root := filepath.VolumeName("/") + string(filepath.Separator)
	if got := repoRootOf(root); got != "" {
		t.Errorf("repoRootOf reported the filesystem root as a work tree: %q", got)
	}
}

func TestOutDirRefusesAnythingInsideTheWorkTree(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		want string
	}{
		{"this repository", wd},
		{"a path that does not exist yet, inside the tree", filepath.Join(wd, "no", "such", "place")},
		{"a relative path inside the tree", "testdata"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OutDir(tc.want)
			if err == nil {
				t.Fatalf("OutDir(%q) = %q, want a refusal", tc.want, got)
			}
			if !strings.Contains(err.Error(), "must never be written where it can be committed") {
				t.Errorf("refused for the wrong reason: %v", err)
			}
		})
	}
}

func TestOutDirDefaultIsUsable(t *testing.T) {
	got, err := OutDir("")
	if err != nil {
		t.Fatalf("the default capture directory was refused: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("default capture directory %q is not absolute", got)
	}
}

func TestOutDirAcceptsAChoiceOutsideAnyRepo(t *testing.T) {
	dir := t.TempDir() // fine here: this test only checks the DECISION, never writes a real capture into it
	got, err := OutDir(dir)
	if err != nil {
		t.Fatalf("OutDir(%q): %v", dir, err)
	}
	if got != dir {
		t.Errorf("OutDir(%q) = %q, want it unchanged", dir, got)
	}
}
