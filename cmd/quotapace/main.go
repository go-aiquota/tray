// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Command quotapace is the fleet's quota PACING gate: a Claude Code
// PreToolUse hook on the Agent tool that refuses a spawn when an account's
// 5-hour or weekly window is being used faster than it can last until its
// reset, so a quota is spent at the rate that empties it exactly at the reset
// instead of days early.
//
// It reads the stamp `aiquota status` leaves at
// os.UserConfigDir()/aiquota/last-status.json (no network, no history file:
// one reading carries used %, reset time and the window's fixed length, which
// is all a pace needs). No stamp, an unreadable one, or one older than
// --max-age ALLOWS: a refusal is not a verdict when the gate cannot read.
//
// Hook contract (https://code.claude.com/docs/en/hooks): the event JSON on
// stdin; to refuse, print {"hookSpecificOutput":{"hookEventName":"PreToolUse",
// "permissionDecision":"deny","permissionDecisionReason":...}} and exit 0.
// Nothing on stdout allows. A spawn of a `checker` agent or a haiku model, and
// a prompt with a `pace-waiver: <reason>` line (logged), always pass.
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
	maxAge                                  time.Duration
	tolerance, minElapsed                   float64

	p       paths     // resolved by resolve
	pathErr error     // why a default path could not be resolved
	warn    io.Writer // where a non-fatal problem is said (stderr)
}

// resolve fills o.p from the flags and the defaults.
func (o *opts) resolve() {
	o.p, o.pathErr = resolvePaths(o.stampPath, o.gatePath, o.waiverLog)
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

// cheap reports whether the spawn is a lookup that always passes.
func (ev hookInput) cheap() bool {
	return ev.ToolInput.SubagentType == "checker" || strings.EqualFold(ev.ToolInput.Model, "haiku")
}

// denyReason returns "" to allow, else the refusal text. Errors reading the
// gate or the stamp allow.
func (o *opts) denyReason(ev hookInput, at time.Time) string {
	if ev.ToolName != "Agent" && ev.ToolName != "Task" {
		return ""
	}
	if ev.cheap() {
		return ""
	}
	o.resolve() // an unresolved path stays empty: nothing is read below, so the hook allows
	g, err := readGate(o.p.gate)
	if err != nil || g.Global.active(at) {
		return ""
	}
	st, err := stamp.Read(o.p.stamp)
	if err != nil || st.Age(at) > o.maxAge || (st.Verdict != stamp.Available && st.Verdict != stamp.Exhausted) {
		return ""
	}
	var lines []string
	measured := 0
	for _, a := range accounts(st, at, o.minElapsed, o.tolerance) {
		if o.account != "" && a.Account != o.account {
			continue
		}
		measured++
		if g.offFor(a.Account, at) {
			return "" // this account has headroom by the owner's decision
		}
		var bad []string
		for _, w := range a.Windows {
			if w.over() {
				bad = append(bad, a.Account+" "+explain(w, at))
			}
		}
		if len(bad) == 0 {
			return "" // one account with headroom: the fleet can spend there
		}
		lines = append(lines, bad...)
	}
	if measured == 0 || len(lines) == 0 {
		return ""
	}
	if reason, ok := waiverReason(ev.ToolInput.Prompt); ok {
		o.logWaiver(reason, at)
		return ""
	}
	return "quota pacing: this spawn is refused, the quota is being used faster than it can last until its reset.\n" +
		strings.Join(lines, "\n") +
		"\nA checker/haiku lookup still passes. Wait, or spawn a checker; to proceed anyway add a line `pace-waiver: <reason>` to the prompt (it is logged);" +
		" disable the gate: quotapace pace off [--for 2h] [--account <label>]"
}

// explain is the one-line numbers of an over-pace window.
func explain(w Pace, at time.Time) string {
	s := fmt.Sprintf("%s: used %.1f%% with %.1f%% of the window elapsed (rate %.2f%%/h, sustainable %.2f%%/h), ", w.Window, w.UsedPercent, w.ElapsedPercent, w.RatePerHour, w.SustainablePerHour)
	if w.State == stateExhausted {
		return s + "exhausted, resets " + w.ResetsAt.Local().Format("Mon 2006-01-02 15:04 MST")
	}
	if w.ProjectedExhaustion != nil {
		s += "projected exhaustion " + w.ProjectedExhaustion.Local().Format("Mon 2006-01-02 15:04 MST") + ", before the reset at "
	} else {
		s += "projected " + fmt.Sprintf("%.0f%%", w.ProjectedAtReset) + " at the reset "
	}
	return s + w.ResetsAt.Local().Format("Mon 2006-01-02 15:04 MST")
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
		Short:         "PreToolUse hook on Agent: refuse a spawn when a quota window is over pace",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ev hookInput
			if err := json.NewDecoder(in).Decode(&ev); err != nil {
				return fmt.Errorf("reading hook JSON: %w", err)
			}
			o.warn = cmd.ErrOrStderr()
			reason := o.denyReason(ev, now())
			if reason == "" {
				return nil
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"hookSpecificOutput": map[string]string{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": reason,
			}})
		},
	}
	f := c.PersistentFlags()
	f.StringVar(&o.stampPath, "stamp", "", "stamp file (default os.UserConfigDir()/aiquota/last-status.json)")
	f.StringVar(&o.gatePath, "gate", "", "switch file (default os.UserConfigDir()/aiquota/pace-gate.json)")
	f.StringVar(&o.waiverLog, "waiver-log", "", "waiver log (default os.UserConfigDir()/aiquota/pace-waivers.log)")
	f.StringVar(&o.account, "account", "", "the account label to judge or switch (default: every account in the stamp; the gate refuses only when all are over pace)")
	f.DurationVar(&o.maxAge, "max-age", 15*time.Minute, "how old a stamp may be before the gate allows for want of a reading")
	f.Float64Var(&o.tolerance, "tolerance", 0.10, "overshoot tolerated: refuse when the projected use at the reset exceeds 100% by more than this fraction")
	f.Float64Var(&o.minElapsed, "min-elapsed", 0.05, "fraction of a window that must have elapsed before a projection counts")
	c.SetVersionTemplate("quotapace {{.Version}}\n")
	c.AddCommand(paceCmd(o))
	return c
}
