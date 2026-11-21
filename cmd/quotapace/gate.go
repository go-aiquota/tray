// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Switch is one off-switch: off, optionally until a moment (zero = until
// `pace on`). An expired Until means the gate is enforced again.
type Switch struct {
	Off   bool      `json:"off"`
	Until time.Time `json:"until,omitempty"`
}

// active reports whether the switch is holding the gate off at now.
func (s Switch) active(now time.Time) bool {
	return s.Off && (s.Until.IsZero() || now.Before(s.Until))
}

// GateState is the persistent switch file: one global switch and one per
// account label.
type GateState struct {
	Global   Switch            `json:"global"`
	Accounts map[string]Switch `json:"accounts,omitempty"`
}

// offFor says whether the gate is off for account by its own switch (the
// global one is judged first by the caller).
func (g GateState) offFor(account string, now time.Time) bool {
	s, ok := g.Accounts[account]
	return ok && account != "" && s.active(now)
}

// defaultGatePath is os.UserConfigDir()/aiquota/pace-gate.json.
func defaultGatePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aiquota", "pace-gate.json"), nil
}

// readGate loads the switch file; a missing file is "everything on". An
// unreadable file is an error the caller decides on (the hook allows).
func readGate(path string) (GateState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return GateState{}, nil
	}
	if err != nil {
		return GateState{}, err
	}
	var g GateState
	if err := json.Unmarshal(data, &g); err != nil {
		return GateState{}, fmt.Errorf("%s: %w", path, err)
	}
	return g, nil
}

// writeGate stores g atomically (temp file + rename), 0600 in a 0700 dir.
func writeGate(path string, g GateState) error {
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pace-gate-*")
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
	return os.Rename(tmp, path)
}

// setSwitch turns the gate off (until = zero for no expiry) or on for account
// ("" = global) and persists it.
func setSwitch(path, account string, off bool, until time.Time) error {
	g, err := readGate(path)
	if err != nil {
		return err
	}
	if account == "" {
		g.Global = Switch{Off: off, Until: until}
		if !off {
			g.Global = Switch{}
		}
	} else if off {
		if g.Accounts == nil {
			g.Accounts = map[string]Switch{}
		}
		g.Accounts[account] = Switch{Off: true, Until: until}
	} else {
		delete(g.Accounts, account)
	}
	return writeGate(path, g)
}

// describe is the one-line status of a switch at now.
func describe(s Switch, now time.Time) string {
	switch {
	case !s.active(now):
		return "on"
	case s.Until.IsZero():
		return "off until `pace on`"
	default:
		return "off until " + s.Until.Local().Format("2006-01-02 15:04 MST")
	}
}
