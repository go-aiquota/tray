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
	stateTooEarly  = "too_early" // too little of the window has elapsed to judge
	stateNoRecent  = "no_recent" // no recent rate is known (fewer than two readings in the lookback): allowed
)

// windowKinds are the general windows; the duration is the product's own
// (menubar/chart.go). A plan reports the windows it has: "Weekly <Model>" is a
// model-specific weekly window (7 days), paced like the others but judging only
// a spawn on that model; a window listed without a value (n/a) is no constraint.
var windowKinds = []struct {
	Name   string
	Prefix string
	Length time.Duration
}{
	{"5h", "session (5", 5 * time.Hour},
	{"7d", "weekly (7", 7 * 24 * time.Hour},
}

// claudeCodeLabel is the account `aiquota status` measures by default: the
// login the running Claude Code process bills to.
const claudeCodeLabel = "Claude Code"

// Pace is one window's measured pace. Rates are percent of the window's limit
// per hour. The gate judges RecentPerHour (the slope over the recorded
// readings of the last hour, nil when unknown) against SustainablePerHour
// (what is left divided by the time to the reset): a burst that has stopped
// stops refusing. RatePerHour is the cumulative average since the window
// began, kept for information: after a burst it stays high for days.
type Pace struct {
	Window              string     `json:"window"`
	Model               string     `json:"model,omitempty"` // set on a model-specific window: it judges only that model
	SlowdownFactor      *float64   `json:"slowdown_factor"` // sustainable / recent when over pace (the share of the recent burn the window can carry), else nil
	UsedPercent         float64    `json:"used_percent"`
	ElapsedPercent      float64    `json:"elapsed_percent"`
	RecentPerHour       *float64   `json:"recent_per_hour"`
	RatePerHour         float64    `json:"rate_per_hour"`
	SustainablePerHour  float64    `json:"sustainable_per_hour"`
	ProjectedAtReset    float64    `json:"projected_at_reset_percent"`
	ProjectedExhaustion *time.Time `json:"projected_exhaustion"`
	ResetsAt            time.Time  `json:"resets_at"`
	State               string     `json:"state"`
}

// compute paces one window at now. minElapsed (a fraction) is the least of the
// window that must have elapsed before a judgement means anything; tol is the
// overshoot tolerated (0.10: the recent rate may exceed the sustainable one by
// 10 %); recent is the recent slope or nil.
func compute(kind string, length time.Duration, used float64, resets, now time.Time, minElapsed, tol float64, recent *float64) Pace {
	p := Pace{Window: kind, UsedPercent: used, ResetsAt: resets, RecentPerHour: recent}
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
	if recent != nil && *recent > 0 && used < 100 {
		eta := now.Add(time.Duration((100 - used) / *recent * float64(time.Hour)))
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
	case left == 0:
		p.State = stateOK // the reset has passed: this reading describes a window that is over
	case recent == nil:
		p.State = stateNoRecent
	case *recent > p.SustainablePerHour*(1+tol):
		p.State = stateOverPace
		f := p.SustainablePerHour / *recent
		p.SlowdownFactor = &f
	default:
		p.State = stateOK
	}
	return p
}

// over reports whether the state refuses a non-cheap spawn.
func (p Pace) over() bool { return p.State == stateOverPace || p.State == stateExhausted }

// kindOf maps a stamp window label to a paced window: its name, its model ("" for
// a general window) and its length; name "" when the label is unpaced.
func kindOf(label string) (name, model string, length time.Duration) {
	l := strings.ToLower(strings.TrimSpace(label))
	for _, k := range windowKinds {
		if strings.HasPrefix(l, k.Prefix) {
			return k.Name, "", k.Length
		}
	}
	if m, ok := strings.CutPrefix(l, "weekly "); ok && m != "" && !strings.HasPrefix(m, "(") {
		return "7d " + m, m, 7 * 24 * time.Hour
	}
	return "", "", 0
}

// reading is one paced window of a stamp. key identifies the account AND its
// plan: the same login on another plan has other windows and other resets, so
// its history must not mix.
type reading struct {
	account, plan, key, name, model string
	length                          time.Duration
	used                            float64
	resets                          time.Time
}

func accountKey(account, plan string) string {
	if plan == "" {
		return account
	}
	return account + "=" + plan
}

// readings lists a stamp's paced windows in stamp order. A window with no reset
// time or no verdict value is skipped: nothing can be judged from it.
func readings(st stamp.Stamp) []reading {
	var out []reading
	for _, w := range st.Windows {
		name, model, length := kindOf(w.Label)
		if name == "" || w.ResetsAtUnix == 0 || (w.Verdict != stamp.Available && w.Verdict != stamp.Exhausted) {
			continue
		}
		used := w.UsedPercent
		if used == 0 && w.Limit > 0 {
			used = w.Used / w.Limit * 100
		}
		out = append(out, reading{w.Account, w.Plan, accountKey(w.Account, w.Plan), name, model, length, used, time.Unix(w.ResetsAtUnix, 0)})
	}
	return out
}

// samplesOf is the stamp as history samples.
func samplesOf(st stamp.Stamp) []Sample {
	var out []Sample
	for _, r := range readings(st) {
		out = append(out, Sample{At: st.At, Account: r.key, Window: r.name, Used: r.used, ResetsAt: r.resets})
	}
	return out
}

// recentFn gives a window's recent slope, nil when unknown.
type recentFn func(account, window string, used float64, resets time.Time) *float64

// AccountPace is one account's paced windows, in stamp order. Active marks the
// account the running Claude Code process bills to: the only one a spawn is
// judged by.
type AccountPace struct {
	Account string
	Plan    string
	Active  bool
	Windows []Pace
}

// activeLabel is the account the stamp was measured for: `aiquota status`
// measures Claude Code's own login by default, so that label when present, else
// the stamp's only account. "" when it cannot be told (several stored accounts):
// nothing then decides a spawn.
func activeLabel(st stamp.Stamp) string {
	seen := map[string]bool{}
	var only string
	for _, w := range st.Windows {
		if w.Account == claudeCodeLabel {
			return w.Account
		}
		seen[w.Account] = true
		only = w.Account
	}
	if len(seen) == 1 {
		return only
	}
	return ""
}

// accounts groups a stamp's measured windows by account and plan, in the order
// the stamp lists them.
func accounts(st stamp.Stamp, now time.Time, minElapsed, tol float64, rc recentFn) []AccountPace {
	var out []AccountPace
	idx := map[string]int{}
	act := activeLabel(st)
	for _, r := range readings(st) {
		i, ok := idx[r.key]
		if !ok {
			i = len(out)
			idx[r.key] = i
			out = append(out, AccountPace{Account: r.account, Plan: r.plan, Active: r.account == act})
		}
		p := compute(r.name, r.length, r.used, r.resets, now, minElapsed, tol, rc(r.key, r.name, r.used, r.resets))
		p.Model = r.model
		out[i].Windows = append(out[i].Windows, p)
	}
	return out
}

// aggregate folds accounts into one pseudo-account by averaging each window
// kind (every account has the same 100 % capacity), so "the fleet's quota" is
// one pace. The recent rate is the mean of the accounts that have one; the
// exhaustion is recomputed, not averaged.
func aggregate(as []AccountPace, now time.Time, minElapsed, tol float64) []Pace {
	var out []Pace
	for _, k := range windowKinds {
		var n, nr int
		var used, resets, recent float64
		for _, a := range as {
			for _, w := range a.Windows {
				if w.Window == k.Name {
					n++
					used += w.UsedPercent
					resets += float64(w.ResetsAt.Unix())
					if w.RecentPerHour != nil {
						nr++
						recent += *w.RecentPerHour
					}
				}
			}
		}
		if n == 0 {
			continue
		}
		var rp *float64
		if nr > 0 {
			m := recent / float64(nr)
			rp = &m
		}
		out = append(out, compute(k.Name, k.Length, used/float64(n), time.Unix(int64(resets/float64(n)), 0), now, minElapsed, tol, rp))
	}
	return out
}
