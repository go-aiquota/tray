// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"strings"
	"time"

	"github.com/go-aiquota/tray/stamp"
)

// Window states, the stable vocabulary of the report and of the deny message.
const (
	stateOK        = "ok"
	stateOverPace  = "over_pace"
	stateExhausted = "exhausted"
	stateTooEarly  = "too_early" // too little of the window has elapsed to project
)

// windowKinds are the two windows the gate paces; per-model weekly windows
// are ignored. The duration is the product's own (menubar/chart.go).
var windowKinds = []struct {
	Name   string
	Prefix string
	Length time.Duration
}{
	{"5h", "session (5", 5 * time.Hour},
	{"7d", "weekly (7", 7 * 24 * time.Hour},
}

// Pace is one window's measured pace. Rates are percent of the window's
// limit per hour; Rate is the AVERAGE since the window began (a stamp holds
// one reading, not a series).
type Pace struct {
	Window              string     `json:"window"`
	UsedPercent         float64    `json:"used_percent"`
	ElapsedPercent      float64    `json:"elapsed_percent"`
	RatePerHour         float64    `json:"rate_per_hour"`
	SustainablePerHour  float64    `json:"sustainable_per_hour"`
	ProjectedAtReset    float64    `json:"projected_at_reset_percent"`
	ProjectedExhaustion *time.Time `json:"projected_exhaustion"`
	ResetsAt            time.Time  `json:"resets_at"`
	State               string     `json:"state"`
}

// compute paces one window at now. minElapsed (a fraction) is the least of the
// window that must have elapsed before a projection means anything; tol is the
// overshoot tolerated (0.10 = projected use may reach 110 % at the reset).
func compute(kind string, length time.Duration, used float64, resets, now time.Time, minElapsed, tol float64) Pace {
	p := Pace{Window: kind, UsedPercent: used, ResetsAt: resets}
	left := resets.Sub(now)
	if left < 0 {
		left = 0
	}
	if left > length {
		left = length
	}
	elapsed := 1 - float64(left)/float64(length)
	p.ElapsedPercent = elapsed * 100
	if left > 0 {
		p.SustainablePerHour = (100 - used) / left.Hours()
		if p.SustainablePerHour < 0 {
			p.SustainablePerHour = 0
		}
	}
	if elapsed > 0 {
		p.RatePerHour = used / (elapsed * length.Hours())
		p.ProjectedAtReset = used / elapsed
	}
	if p.RatePerHour > 0 && used < 100 {
		eta := now.Add(time.Duration((100 - used) / p.RatePerHour * float64(time.Hour)))
		if eta.Before(resets) {
			p.ProjectedExhaustion = &eta
		}
	}
	switch {
	case used >= 100:
		p.State = stateExhausted
		t := now
		p.ProjectedExhaustion = &t
	case elapsed < minElapsed:
		p.State = stateTooEarly
		p.ProjectedExhaustion = nil
	case p.ProjectedAtReset > 100*(1+tol):
		p.State = stateOverPace
	default:
		p.State = stateOK
	}
	return p
}

// over reports whether the state refuses a non-cheap spawn.
func (p Pace) over() bool { return p.State == stateOverPace || p.State == stateExhausted }

// kindOf maps a stamp window label to a paced window name, "" when unpaced.
func kindOf(label string) (string, time.Duration) {
	l := strings.ToLower(label)
	for _, k := range windowKinds {
		if strings.HasPrefix(l, k.Prefix) {
			return k.Name, k.Length
		}
	}
	return "", 0
}

// AccountPace is one account's paced windows, in 5h, 7d order.
type AccountPace struct {
	Account string
	Windows []Pace
}

// accounts groups a stamp's measured windows by account label, in the order
// the stamp lists them. A window with no reset time or no verdict value is
// skipped: nothing can be projected from it.
func accounts(st stamp.Stamp, now time.Time, minElapsed, tol float64) []AccountPace {
	var out []AccountPace
	idx := map[string]int{}
	for _, w := range st.Windows {
		name, length := kindOf(w.Label)
		if name == "" || w.ResetsAtUnix == 0 || (w.Verdict != stamp.Available && w.Verdict != stamp.Exhausted) {
			continue
		}
		used := w.UsedPercent
		if used == 0 && w.Limit > 0 {
			used = w.Used / w.Limit * 100
		}
		i, ok := idx[w.Account]
		if !ok {
			i = len(out)
			idx[w.Account] = i
			out = append(out, AccountPace{Account: w.Account})
		}
		out[i].Windows = append(out[i].Windows, compute(name, length, used, time.Unix(w.ResetsAtUnix, 0), now, minElapsed, tol))
	}
	return out
}

// aggregate folds accounts into one pseudo-account by averaging each window
// kind (every account has the same 100 % capacity), so "the fleet's quota" is
// one pace. The exhaustion is recomputed from the mean, not averaged.
func aggregate(as []AccountPace, now time.Time, minElapsed, tol float64) []Pace {
	var out []Pace
	for _, k := range windowKinds {
		var n int
		var used, resets float64
		for _, a := range as {
			for _, w := range a.Windows {
				if w.Window == k.Name {
					n++
					used += w.UsedPercent
					resets += float64(w.ResetsAt.Unix())
				}
			}
		}
		if n == 0 {
			continue
		}
		out = append(out, compute(k.Name, k.Length, used/float64(n), time.Unix(int64(resets/float64(n)), 0), now, minElapsed, tol))
	}
	return out
}
