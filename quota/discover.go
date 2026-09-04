// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package quota

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// BinaryPrefix is the naming convention every provider plugin binary
// follows: go-aiquota-plugin-<provider> (go-aiquota-plugin-<provider>.exe
// on Windows). A plugin registers itself with the host simply by existing
// on PATH under this name and implementing the QuotaProvider contract —
// there is no separate registration step, manifest file, or host code
// change involved in adding a new one.
const BinaryPrefix = "go-aiquota-plugin-"

// DiscoverProviders returns the provider name every go-aiquota-plugin-*
// executable on path (an os.Getenv("PATH")-shaped, os.PathListSeparator-
// joined string — a parameter rather than reading the environment directly,
// so a test can supply one without mutating process state) declares — the
// part of its filename after BinaryPrefix — deduplicated and sorted for a
// stable menu order. A PATH entry that does not exist or cannot be read is
// skipped rather than treated as an error, mirroring exec.LookPath's own
// tolerance for a stale entry.
func DiscoverProviders(path string) []string {
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(path) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if runtime.GOOS == "windows" {
				name = strings.TrimSuffix(name, ".exe")
			}
			provider := strings.TrimPrefix(name, BinaryPrefix)
			if provider == name || provider == "" {
				continue // no BinaryPrefix, or nothing after it
			}
			seen[provider] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
