// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// A stamp holds ONE reading, so a rate over the last hour needs a series: the
// gate appends every new reading it sees to a small history file (next to the
// stamp) and measures the slope between the oldest reading of the lookback and
// the newest. The file is a convenience, never a verdict: unreadable, empty or
// too short a history means "no recent rate", which allows.

const (
	historyKeep = 24 * time.Hour // older samples are dropped
	historyMax  = 2000           // hard cap on the file
	resetSlack  = 5 * time.Minute
)

// Sample is one account's window as read from one stamp.
type Sample struct {
	At       time.Time `json:"at"`
	Account  string    `json:"account"`
	Window   string    `json:"window"`
	Used     float64   `json:"used_percent"`
	ResetsAt time.Time `json:"resets_at"`
}

// History is the persisted series.
type History struct {
	Samples []Sample `json:"samples"`
}

// readHistory loads the file; anything wrong yields an empty history.
func readHistory(path string) History {
	var h History
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &h) != nil {
		return History{}
	}
	return h
}

// add appends the samples that are newer than the newest already held for
// their (account, window), prunes by age and count, and says whether anything
// changed.
func (h History) add(ss []Sample) (History, bool) {
	type key struct{ account, window string }
	last := map[key]time.Time{}
	var newest time.Time
	for _, s := range h.Samples {
		k := key{s.Account, s.Window}
		if s.At.After(last[k]) {
			last[k] = s.At
		}
	}
	out := History{Samples: append([]Sample(nil), h.Samples...)}
	changed := false
	for _, s := range ss {
		if !s.At.After(last[key{s.Account, s.Window}]) {
			continue
		}
		out.Samples = append(out.Samples, s)
		changed = true
	}
	if !changed {
		return h, false
	}
	for _, s := range out.Samples {
		if s.At.After(newest) {
			newest = s.At
		}
	}
	kept := out.Samples[:0:0]
	for _, s := range out.Samples {
		if newest.Sub(s.At) <= historyKeep {
			kept = append(kept, s)
		}
	}
	if len(kept) > historyMax {
		kept = kept[len(kept)-historyMax:]
	}
	out.Samples = kept
	return out, true
}

// recent is the slope in percent per hour between the oldest sample inside
// [end-lookback, end) of the same window instance and the reading (used at
// end), or nil when there is no such sample or it is closer than minSpan to
// end (one tick of the counter is not a rate).
func (h History) recent(account, window string, used float64, resets, end time.Time, lookback, minSpan time.Duration) *float64 {
	var oldest Sample
	found := false
	for _, s := range h.Samples {
		if s.Account != account || s.Window != window || s.At.Before(end.Add(-lookback)) || !s.At.Before(end) {
			continue
		}
		if d := s.ResetsAt.Sub(resets); d > resetSlack || d < -resetSlack || s.Used > used {
			continue // another instance of the window, or a counter that went back
		}
		if !found || s.At.Before(oldest.At) {
			oldest, found = s, true
		}
	}
	if !found || end.Sub(oldest.At) < minSpan {
		return nil
	}
	r := (used - oldest.Used) / end.Sub(oldest.At).Hours()
	return &r
}

// writeHistory replaces the file atomically (a reader never sees half of it).
func writeHistory(path string, h History) error {
	data, _ := json.Marshal(h) // plain structs cannot fail to encode
	tmp := path + ".tmp"
	err := writeFileIn(tmp, data, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("saving the history: %w", err)
	}
	return nil
}
