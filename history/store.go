// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package history persists a rolling window of each account's own quota
// usage over time — session/weekly percentages, sampled once per poll —
// so a person can see pressure/progression, not just a current snapshot.
// It is written far more often than account.Store's accounts.json (every
// poll, indefinitely, rather than only on add/remove), so unlike that
// file's plain os.WriteFile, this one writes through a temp file and
// os.Rename: a crash mid-write here is a real possibility over the
// file's lifetime, not a theoretical one.
package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Retention is how long a point is kept before Append prunes it — long
// enough to span a full weekly-window reset cycle plus slack.
const Retention = 8 * 24 * time.Hour

// Point is one sample: a window's used/limit at a moment in time.
// AtUnix mirrors quotapb.QuotaWindow.ResetsAtUnix's own unix-seconds
// convention rather than introducing a second time encoding.
type Point struct {
	AtUnix int64   `json:"at"`
	Used   float64 `json:"used"`
	Limit  float64 `json:"limit"`
}

// Store persists points to one JSON file at Path: a map[string][]Point
// keyed by seriesKey(accountID, windowLabel). Not safe for concurrent use.
type Store struct {
	Path string
}

// DefaultPath returns the standard history.json location:
// os.UserConfigDir()/go-aiquota/history.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "go-aiquota", "history.json"), nil
}

// seriesKey is the map key one (account, window) series is stored under.
// NUL-joined rather than a printf-style separator: an account id or
// window label that happened to contain the separator itself would
// silently collide two different series into one.
func seriesKey(accountID, windowLabel string) string {
	return accountID + "\x00" + windowLabel
}

// Append records p for (accountID, windowLabel), prunes anything older
// than Retention (measured against p.AtUnix, not time.Now(), so a
// backdated point in a test prunes exactly as a live one would), and
// writes the file. A series' points are kept in ascending AtUnix order.
func (s *Store) Append(accountID, windowLabel string, p Point) error {
	all, err := s.readAll()
	if err != nil {
		return err
	}
	key := seriesKey(accountID, windowLabel)
	pts := append(all[key], p)
	sort.Slice(pts, func(i, j int) bool { return pts[i].AtUnix < pts[j].AtUnix })

	cutoff := p.AtUnix - int64(Retention.Seconds())
	kept := pts[:0]
	for _, pt := range pts {
		if pt.AtUnix >= cutoff {
			kept = append(kept, pt)
		}
	}
	all[key] = kept
	return s.writeAll(all)
}

// Recent returns (accountID, windowLabel)'s points at or after since, in
// ascending AtUnix order. A series with no points (never polled, or
// polled but since pruned) returns nil, not an error.
func (s *Store) Recent(accountID, windowLabel string, since time.Time) ([]Point, error) {
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}
	cutoff := since.Unix()
	var out []Point
	for _, pt := range all[seriesKey(accountID, windowLabel)] {
		if pt.AtUnix >= cutoff {
			out = append(out, pt)
		}
	}
	return out, nil
}

// readAll reads the store's file, treating "does not exist yet" as an
// empty store (the common first-run case) rather than an error.
func (s *Store) readAll() (map[string][]Point, error) {
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return map[string][]Point{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("history: reading %s: %w", s.Path, err)
	}
	var all map[string][]Point
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("history: parsing %s: %w", s.Path, err)
	}
	if all == nil {
		all = map[string][]Point{}
	}
	return all, nil
}

// writeAll writes the whole store atomically: a temp file in the SAME
// directory (so the final rename is one filesystem, not a cross-device
// copy) is written and closed, THEN renamed over the real path — a
// reader (or a crash) never sees a half-written file, only the old one
// or the new one.
func (s *Store) writeAll(all map[string][]Point) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("history: creating %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return fmt.Errorf("history: encoding: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".history-*.json.tmp")
	if err != nil {
		return fmt.Errorf("history: creating a temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("history: writing %s: %w", tmpPath, writeErr)
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("history: closing %s: %w", tmpPath, closeErr)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("history: setting permissions on %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("history: renaming %s to %s: %w", tmpPath, s.Path, err)
	}
	return nil
}
