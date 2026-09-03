// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package capture

import (
	"fmt"
	"os"
	"path/filepath"
)

// OutDirEnv overrides where a capture file is written. Still checked
// against the same rule as the default: never inside a git work tree.
const OutDirEnv = "GO_AIQUOTA_CAPTURE_DIR"

// OutDir is where a capture file may be written: durable, so a person can
// come back and read it after the process that made it has exited, and
// never inside a git work tree. This is the same discipline
// go-macos/screencapture applies to a screen capture, extended here for
// the same underlying reason: a capture from this package holds a real,
// logged-in account's real (redacted, but not empty) request/response
// traffic — data about a real person's account, not just pixels — and a
// .gitignore entry is a safety net, not a barrier: git add -f, a fresh
// clone, or any tool that does not consult it publishes the file anyway.
//
// want is the caller's choice, or "" to use the default
// (os.UserConfigDir()/go-aiquota/captures, overridable via OutDirEnv).
func OutDir(want string) (string, error) {
	dir, chosen := want, OutDirEnv
	if dir == "" {
		chosen = "the default capture directory"
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("no user configuration directory to keep captures in: %w", err)
		}
		dir = filepath.Join(base, "go-aiquota", "captures")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("%s (%q): %w", chosen, dir, err)
	}
	if root := repoRootOf(abs); root != "" {
		return "", fmt.Errorf("%s (%q) is inside the git work tree at %s; "+
			"a capture of real account traffic must never be written where it can be committed",
			chosen, abs, root)
	}
	return abs, nil
}

// repoRootOf returns the work tree dir is inside, or "" if it is in none.
// It walks all the way to the filesystem root: a capture directory several
// levels below a checkout is still in the checkout.
func repoRootOf(dir string) string {
	for d := dir; ; {
		// A .git that is a FILE is a worktree or a submodule, and commits
		// just as well as a directory does.
		if fi, err := os.Stat(filepath.Join(d, ".git")); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}
