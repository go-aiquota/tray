// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package capture

import (
	"github.com/go-appdirs/outdir"
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
	// ⛔ The decision moved to go-appdirs/outdir, which owns the question and
	// is used by go-macos/screencapture, go-mswin/screencapture and
	// go-widgets/window too. The same sixty lines lived in all four, and
	// adopting the shared one CLOSED A HOLE: this copy walked up from the
	// path as given, resolving nothing, so a directory reached through a
	// symbolic link found no work tree and was accepted. outdir resolves the
	// path first and refuses it.
	//
	// That matters more here than anywhere else in the family: a capture from
	// this package is not pixels, it is a logged-in account's real traffic.
	return outdir.Choose(outdir.Spec{
		App:  "go-aiquota",
		Env:  OutDirEnv,
		Sub:  "captures",
		Want: want,
	})
}

// repoRootOf returns the work tree dir is inside, or "" if it is in none.
// It walks all the way to the filesystem root: a capture directory several
// levels below a checkout is still in the checkout.
// repoRootOf is outdir's, kept so the tests below read as they did.
func repoRootOf(dir string) string { return outdir.RepoRootOf(dir) }
