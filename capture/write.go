// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package capture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WriteEntries JSON-encodes entries and writes them to a new,
// timestamp-named file under dir (see OutDir; this does not call it —
// callers choose when to resolve and create the directory), creating dir
// if it doesn't exist yet. Returns the file's path.
func WriteEntries(dir string, entries []Entry) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("capture: creating %s: %w", dir, err)
	}
	name := fmt.Sprintf("capture-%s.json", time.Now().UTC().Format("20060102-150405.000"))
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return "", fmt.Errorf("capture: encoding %d entries: %w", len(entries), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("capture: writing %s: %w", path, err)
	}
	return path, nil
}
