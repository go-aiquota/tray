// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package stamp is the file `aiquota status` leaves behind and the
// `quotaclaim` hook reads: the moment a usage limit was last MEASURED, per
// window, with a verdict. It holds no credential — only percentages, reset
// times and the plan name.
//
// The gate exists because a limit asserted from memory or from an old error
// message ("resets Nov 12") outlived the plan change that made it false
// (2026-11-09). A claim about a limit is admissible only next to a recent
// measurement; this file is that measurement's receipt.
package stamp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Verdict is the three-valued answer. Unknown is never folded into either of
// the others: "could not read" is not "available" and not "exhausted".
type Verdict string

const (
	Available Verdict = "available"
	Exhausted Verdict = "exhausted"
	Unknown   Verdict = "unknown"
)

// Version is the schema version written to every stamp.
const Version = 1

// Window is one measured usage window.
type Window struct {
	Account      string  `json:"account"`
	Label        string  `json:"label"`
	Used         float64 `json:"used"`
	Limit        float64 `json:"limit"`
	UsedPercent  float64 `json:"used_percent"`
	ResetsAtUnix int64   `json:"resets_at_unix"`
	Verdict      Verdict `json:"verdict"`
	Note         string  `json:"note,omitempty"`
}

// Stamp is the whole receipt of one `aiquota status` run.
type Stamp struct {
	Version  int       `json:"version"`
	At       time.Time `json:"at"`
	Plans    []string  `json:"plans,omitempty"`
	Windows  []Window  `json:"windows"`
	Verdict  Verdict   `json:"verdict"`
	Problems []string  `json:"problems,omitempty"`
}

// Overall folds window verdicts: exhausted beats unknown beats available, and
// no window at all is unknown (nothing was measured).
func Overall(ws []Window) Verdict {
	if len(ws) == 0 {
		return Unknown
	}
	out := Available
	for _, w := range ws {
		switch w.Verdict {
		case Exhausted:
			return Exhausted
		case Available:
		default:
			out = Unknown
		}
	}
	return out
}

// DefaultPath is os.UserConfigDir()/aiquota/last-status.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aiquota", "last-status.json"), nil
}

// Write stores s at path atomically (temp file in the same directory, then
// rename), mode 0600 in a 0700 directory.
func Write(path string, s Stamp) error {
	if s.Version == 0 {
		s.Version = Version
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".last-status-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ErrNoStamp means no measurement was ever recorded at the path.
var ErrNoStamp = errors.New("stamp: no measurement recorded")

// Read loads the stamp at path. A missing file is ErrNoStamp.
func Read(path string) (Stamp, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Stamp{}, ErrNoStamp
	}
	if err != nil {
		return Stamp{}, err
	}
	var s Stamp
	if err := json.Unmarshal(data, &s); err != nil {
		return Stamp{}, fmt.Errorf("stamp: %s: %w", path, err)
	}
	return s, nil
}

// Age is how old the measurement is at now (never negative).
func (s Stamp) Age(now time.Time) time.Duration {
	if d := now.Sub(s.At); d > 0 {
		return d
	}
	return 0
}
