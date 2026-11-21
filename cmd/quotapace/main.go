// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Command quotapace is the fleet's quota PACING gate: a Claude Code
// PreToolUse hook on the Agent tool that paces the quota of the plan in use: a
// window being used faster than it can last until its reset makes the hook
// advise the session to slow down (more, sonnet or haiku sub-agents) and refuse
// only an opus sub-agent, so a quota is spent at the rate that empties it
// exactly at the reset instead of days early.
//
// Only the ACTIVE account decides (the login Claude Code bills to, which
// `aiquota status` measures; it is re-read at every call, so a switch of account
// during the day is followed), by exactly the windows its plan reports: 5h and
// weekly, plus a model-specific "Weekly <Model>" window when the plan has one,
// which judges only a spawn on that model. A window listed without a value is no
// constraint; an unknown plan allows.
//
// It reads the stamp `aiquota status` leaves at
// os.UserConfigDir()/aiquota/last-status.json and judges the RECENT rate (the
// slope over the readings it recorded in pace-history.json during the last
// hour) against the sustainable one (what is left divided by the time to the
// reset): a burst that has stopped stops refusing. No stamp, an unreadable
// one, one older than --max-age, or fewer than two recent readings ALLOWS: a
// refusal is not a verdict when the gate cannot read. A missing or stale stamp
// is refreshed by the hook itself (see refresh.go).
//
// Hook contract (https://code.claude.com/docs/en/hooks): the event JSON on
// stdin; to refuse, print {"hookSpecificOutput":{"hookEventName":"PreToolUse",
// "permissionDecision":"deny","permissionDecisionReason":...}} and exit 0.
// Nothing on stdout allows. Over pace, a non-opus spawn is allowed with an
// advisory (additionalContext), and an opus one is refused unless its prompt has
// a `pace-waiver: <reason>` line (logged).
//
//	quotapace pace [--json]                     per account and aggregate report
//	quotapace pace off [--for 2h] [--account L] switch the gate off
//	quotapace pace on [--account L]             switch it back on
//	quotapace pace status                       the switches
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-aiquota/tray/internal/buildver"
	"github.com/go-aiquota/tray/stamp"
)

var osExit = os.Exit

var now = time.Now

// hookInput is the subset of the PreToolUse event used here.
type hookInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		SubagentType string `json:"subagent_type"`
		Model        string `json:"model"`
		Prompt       string `json:"prompt"`
	} `json:"tool_input"`
}

// opts are the flags every command shares.
type opts struct {
	stampPath, gatePath, account, waiverLog string
	historyPath, refreshState, refreshCmd   string
	maxAge                                  time.Duration
	recentWindow, minSpan                   time.Duration
	refreshEvery, refreshTimeout            time.Duration
	tolerance, minElapsed                   float64
	maxAgents                               int

	p       paths     // resolved by resolve
	pathErr error     // why a default path could not be resolved
	warn    io.Writer // where a non-fatal problem is said (stderr)
}

// resolve fills o.p from the flags and the defaults.
func (o *opts) resolve() {
	o.p, o.pathErr = resolvePaths(o.stampPath, o.gatePath, o.waiverLog, o.historyPath, o.refreshState)
}

// recentFn records the stamp's readings in the history (a failure is said, never
// fatal) and returns the recent slope of any window of that stamp.
func (o *opts) recentFn(st stamp.Stamp) recentFn {
	var h History
	if o.p.history != "" {
		h = readHistory(o.p.history)
		if grown, changed := h.add(samplesOf(st)); changed {
			h = grown
			if err := writeHistory(o.p.history, h); err != nil {
				fmt.Fprintln(o.warn, "quotapace: history not saved:", err)
			}
		}
	}
	return func(account, window string, used float64, resets time.Time) *float64 {
		return h.recent(account, window, used, resets, st.At, o.recentWindow, o.minSpan)
	}
}

// waiverReason returns the reason of the first `pace-waiver: <reason>` line.
func waiverReason(prompt string) (string, bool) {
	for _, line := range strings.Split(prompt, "\n") {
		l := strings.TrimSpace(line)
		if r, ok := strings.CutPrefix(l, "pace-waiver:"); ok && strings.TrimSpace(r) != "" {
			return strings.TrimSpace(r), true
		}
	}
	return "", false
}

// opus reports whether the spawn runs on the most expensive model: an explicit
// opus model, or the `reviewer` agent type (opus) with no model override.
// Every other delegation is cheaper than doing the same work in the main
// session (which is opus with the largest context), so it is never refused:
// refusing it would move the same tokens to the dearest place.
func (ev hookInput) opus() bool { return ev.runsOn("opus") }

// runsOn reports whether the spawn runs on the model family (a substring of the
// model name, lower case); the `reviewer` agent type with no model is opus.
func (ev hookInput) runsOn(model string) bool {
	m := strings.ToLower(ev.ToolInput.Model)
	if m == "" && ev.ToolInput.SubagentType == "reviewer" {
		m = "opus"
	}
	return model != "" && strings.Contains(m, model)
}

// decision is what the hook says: Deny blocks the spawn, Advice is a
// non-blocking note for the session. Both empty allows silently.
type decision struct{ Deny, Advice string }

// decide judges a spawn. Errors reading the gate or the stamp allow.
func (o *opts) decide(ev hookInput, at time.Time) decision {
	if ev.ToolName != "Agent" && ev.ToolName != "Task" {
		return decision{}
	}
	o.resolve() // an unresolved path stays empty: nothing is read below, so the hook allows
	g, err := readGate(o.p.gate)
	if err != nil || g.Global.active(at) {
		return decision{}
	}
	st, err := stamp.Read(o.p.stamp)
	if err != nil || st.Age(at) > o.maxAge {
		o.refresh(at) // missing or stale: ask for a new reading (single flight, bounded, throttled)
		st, err = stamp.Read(o.p.stamp)
	}
	if err != nil || st.Age(at) > o.maxAge || (st.Verdict != stamp.Available && st.Verdict != stamp.Exhausted) {
		return decision{}
	}
	// Only the account in use decides, by the windows its plan reports: a plan
	// with unknown name, or another account in the stamp, never refuses.
	var lines []string
	slow := 1.0
	for _, a := range accounts(st, at, o.minElapsed, o.tolerance, o.recentFn(st)) {
		if (o.account == "" && !a.Active) || (o.account != "" && a.Account != o.account) {
			continue
		}
		if a.Plan == "" || g.offFor(a.Account, at) {
			return decision{} // an unknown plan has no known windows; or the owner says there is headroom
		}
		for _, w := range a.Windows {
			if w.over() && (w.Model == "" || ev.runsOn(w.Model)) {
				lines = append(lines, fmt.Sprintf("%s (%s plan) %s", a.Account, a.Plan, explain(w, at)))
				if w.SlowdownFactor != nil && *w.SlowdownFactor < slow {
					slow = *w.SlowdownFactor
				}
			}
		}
	}
	if len(lines) == 0 {
		return decision{}
	}
	body := strings.Join(lines, "\n")
	// The concurrency follows from the plan's own windows: the recent burn must
	// shrink to the share they can carry (slow), applied to the usual concurrency.
	n := int(float64(o.maxAgents) * slow)
	if n < 1 {
		n = 1
	}
	advice := fmt.Sprintf("quota pacing: the quota is being used faster than it can last until its reset.\n%s\nThe plan's windows carry %.0f%% of the recent burn: keep at most %d sub-agents running at once (of %d usually) and prefer sonnet or haiku ones: they cost less than doing the work in this session.", body, slow*100, n, o.maxAgents)
	if !ev.opus() {
		return decision{Advice: advice}
	}
	if reason, ok := waiverReason(ev.ToolInput.Prompt); ok {
		o.logWaiver(reason, at)
		return decision{Advice: advice}
	}
	return decision{Deny: "quota pacing: this opus sub-agent is refused, the quota is being used faster than it can last until its reset.\n" + body +
		"\nSpawn it on sonnet instead (model: sonnet, or an implementer agent instead of a reviewer): that is cheaper than opus and cheaper than doing the work here. " +
		"To use opus anyway add a line `pace-waiver: <reason>` to the prompt (it is logged); " +
		"disable the gate: quotapace pace off [--for 2h] [--account <label>]"}
}

// explain is the one-line numbers of an over-pace window.
func explain(w Pace, at time.Time) string {
	s := fmt.Sprintf("%s: used %.1f%% with %.1f%% of the window elapsed (sustainable %.2f%%/h, cumulative %.2f%%/h", w.Window, w.UsedPercent, w.ElapsedPercent, w.SustainablePerHour, w.RatePerHour)
	if w.RecentPerHour != nil {
		s += fmt.Sprintf(", recent %.2f%%/h", *w.RecentPerHour)
	}
	s += "), "
	reset := w.ResetsAt.Local().Format("Mon 2006-01-02 15:04 MST")
	if w.State == stateExhausted {
		return s + "exhausted, resets " + reset
	}
	return s + "at the recent rate the quota is gone by " + w.ProjectedExhaustion.Local().Format("Mon 2006-01-02 15:04 MST") + ", before the reset at " + reset
}

func (o *opts) logWaiver(reason string, at time.Time) {
	err := o.pathErr // the waiver path is unresolved exactly when this is set and no flag gave it
	if o.p.waiver != "" {
		line, _ := json.Marshal(map[string]string{"at": at.UTC().Format(time.RFC3339), "reason": reason})
		err = writeFileIn(o.p.waiver, append(line, '\n'), os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	}
	if err != nil { // the waiver still passes: the gate never refuses over its own log
		fmt.Fprintln(o.warn, "quotapace: waiver not logged:", err)
	}
}

func main() { osExit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, in io.Reader, out, errw io.Writer) int {
	cmd := RootCmd(in)
	cmd.SetArgs(args)
	cmd.SetOut(out)
	cmd.SetErr(errw)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(errw, "quotapace:", err)
		return 1 // non-blocking error per the hooks contract: never wedge a session on our own bug
	}
	return 0
}

// RootCmd builds the hook command and its `pace` subcommands.
func RootCmd(in io.Reader) *cobra.Command {
	o := &opts{}
	c := &cobra.Command{
		Use:           "quotapace",
		Version:       buildver.Version(),
		Short:         "PreToolUse hook on Agent: advise slowing down, and refuse an opus spawn, when the active plan's quota is over pace",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ev hookInput
			if err := json.NewDecoder(in).Decode(&ev); err != nil {
				return fmt.Errorf("reading hook JSON: %w", err)
			}
			o.warn = cmd.ErrOrStderr()
			d := o.decide(ev, now())
			out := map[string]string{"hookEventName": "PreToolUse"}
			switch {
			case d.Deny != "":
				out["permissionDecision"] = "deny"
				out["permissionDecisionReason"] = d.Deny
			case d.Advice != "":
				out["additionalContext"] = d.Advice // no permissionDecision: the spawn goes ahead
			default:
				return nil
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"hookSpecificOutput": out})
		},
	}
	f := c.PersistentFlags()
	f.StringVar(&o.stampPath, "stamp", "", "stamp file (default os.UserConfigDir()/aiquota/last-status.json)")
	f.StringVar(&o.gatePath, "gate", "", "switch file (default os.UserConfigDir()/aiquota/pace-gate.json)")
	f.StringVar(&o.waiverLog, "waiver-log", "", "waiver log (default os.UserConfigDir()/aiquota/pace-waivers.log)")
	f.StringVar(&o.account, "account", "", "the account label to switch, or to judge instead of the active one (default: the account Claude Code bills to, as `aiquota status` measures it)")
	f.StringVar(&o.historyPath, "history", "", "readings history (default os.UserConfigDir()/aiquota/pace-history.json)")
	f.StringVar(&o.refreshState, "refresh-state", "", "last refresh attempt; its .lock is the single-flight lock (default os.UserConfigDir()/aiquota/pace-refresh.json)")
	f.StringVar(&o.refreshCmd, "refresh-cmd", "", "program that writes the stamp, run as `<cmd> status --stamp <file>` (default: aiquota beside this executable, else on PATH)")
	f.DurationVar(&o.refreshEvery, "refresh-every", 5*time.Minute, "at most one live refresh per this long, even when every one fails (0 disables the refresh)")
	f.DurationVar(&o.refreshTimeout, "refresh-timeout", 3*time.Second, "longest the hook waits for a refresh (the refresher is left to finish)")
	f.IntVar(&o.maxAgents, "max-agents", 3, "your usual sub-agent concurrency; the advisory scales it by the share of the recent burn the plan's windows can carry")
	f.DurationVar(&o.recentWindow, "recent", time.Hour, "how far back the recent rate looks")
	f.DurationVar(&o.minSpan, "min-span", 5*time.Minute, "least time between two readings for their slope to count (a counter ticking in whole percents is not a rate over less)")
	f.DurationVar(&o.maxAge, "max-age", 15*time.Minute, "how old a stamp may be before the gate allows for want of a reading")
	f.Float64Var(&o.tolerance, "tolerance", 0.10, "overshoot tolerated: refuse when the recent rate exceeds the sustainable one by more than this fraction")
	f.Float64Var(&o.minElapsed, "min-elapsed", 0.05, "fraction of a window that must have elapsed before a projection counts")
	c.SetVersionTemplate("quotapace {{.Version}}\n")
	c.AddCommand(paceCmd(o))
	return c
}
