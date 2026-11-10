// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package buildver is the version the programs of this module report with
// --version: the release tag a release build links in
// (-ldflags "-X github.com/go-aiquota/tray/internal/buildver.version=v0.8.2"),
// else the module version Go recorded (`go install ...@v0.8.2` has it), else
// "(devel)".
package buildver

import "runtime/debug"

var version string

// Version returns the version to report.
func Version() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}
