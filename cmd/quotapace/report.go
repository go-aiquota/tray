// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-aiquota/tray/stamp"
)

// ReportSchema is the version of the `pace --json` document. Fields are only
// ever added within a version.
const ReportSchema = 1

// Report is the stable `pace --json` document.
type Report struct {
	Schema          int             `json:"schema"`
	At              time.Time       `json:"at"`
	StampAt         time.Time       `json:"stamp_at"`
	StampAgeSeconds int64           `json:"stamp_age_seconds"`
	StampFresh      bool            `json:"stamp_fresh"` // within --max-age; a stale stamp makes the hook allow
	Gate            GateReport      `json:"gate"`
	Accounts        []AccountReport `json:"accounts"`
	Aggregate       []Pace          `json:"aggregate"`
}

// GateReport is a switch as reported: State "on" or "off", Until set for a
// timed off.
type GateReport struct {
	State string     `json:"state"`
	Until *time.Time `json:"until"`
}

// AccountReport is one account (a label, never an email or id).
type AccountReport struct {
	Account string     `json:"account"`
	Gate    GateReport `json:"gate"`
	Windows []Pace     `json:"windows"`
}

func gateReport(s Switch, at time.Time) GateReport {
	if !s.active(at) {
		return GateReport{State: "on"}
	}
	r := GateReport{State: "off"}
	if !s.Until.IsZero() {
		u := s.Until
		r.Until = &u
	}
	return r
}

func (o *opts) report(at time.Time) (Report, error) {
	sp, err := o.stamp()
	if err != nil {
		return Report{}, err
	}
	st, err := stamp.Read(sp)
	if err != nil {
		return Report{}, err
	}
	gp, err := o.gate()
	if err != nil {
		return Report{}, err
	}
	g, err := readGate(gp)
	if err != nil {
		return Report{}, err
	}
	r := Report{Schema: ReportSchema, At: at.UTC(), StampAt: st.At.UTC(), StampAgeSeconds: int64(st.Age(at).Seconds()),
		StampFresh: st.Age(at) <= o.maxAge, Gate: gateReport(g.Global, at), Accounts: []AccountReport{}}
	as := accounts(st, at, o.minElapsed, o.tolerance)
	for _, a := range as {
		r.Accounts = append(r.Accounts, AccountReport{Account: a.Account, Gate: gateReport(g.Accounts[a.Account], at), Windows: a.Windows})
	}
	r.Aggregate = aggregate(as, at, o.minElapsed, o.tolerance)
	if r.Aggregate == nil {
		r.Aggregate = []Pace{}
	}
	return r, nil
}

func writePaceLine(w io.Writer, indent string, p Pace) {
	exh := "none before the reset"
	if p.ProjectedExhaustion != nil {
		exh = p.ProjectedExhaustion.Local().Format("Mon 2006-01-02 15:04 MST")
	}
	fmt.Fprintf(w, "%s%-3s used %5.1f%%  elapsed %5.1f%%  rate %5.2f%%/h  sustainable %5.2f%%/h  projected %6.1f%% at reset  exhaustion: %s  [%s]\n",
		indent, p.Window, p.UsedPercent, p.ElapsedPercent, p.RatePerHour, p.SustainablePerHour, p.ProjectedAtReset, exh, p.State)
}

func gateText(g GateReport) string {
	if g.State == "on" {
		return "on"
	}
	if g.Until == nil {
		return "off until `pace on`"
	}
	return "off until " + g.Until.Local().Format("2006-01-02 15:04 MST")
}

func writeReport(w io.Writer, r Report) {
	fresh := "fresh"
	if !r.StampFresh {
		fresh = "STALE, the hook allows"
	}
	fmt.Fprintf(w, "stamp: %s, %ds old (%s)\ngate: %s\n", r.StampAt.Local().Format("2006-01-02 15:04 MST"), r.StampAgeSeconds, fresh, gateText(r.Gate))
	for _, a := range r.Accounts {
		fmt.Fprintf(w, "%s (gate: %s)\n", a.Account, gateText(a.Gate))
		for _, p := range a.Windows {
			writePaceLine(w, "  ", p)
		}
	}
	if len(r.Accounts) > 1 {
		fmt.Fprintln(w, "aggregate (mean of the accounts)")
	} else {
		fmt.Fprintln(w, "aggregate")
	}
	for _, p := range r.Aggregate {
		writePaceLine(w, "  ", p)
	}
}

// paceCmd is `pace` (the report) with its off / on / status children.
func paceCmd(o *opts) *cobra.Command {
	var asJSON bool
	var forDur time.Duration
	c := &cobra.Command{
		Use:   "pace",
		Short: "Report the pace of every account's 5h and weekly window (--json for the stable schema)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := o.report(now())
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(r)
			}
			writeReport(cmd.OutOrStdout(), r)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON (schema "+fmt.Sprint(ReportSchema)+")")

	off := &cobra.Command{
		Use:   "off",
		Short: "Switch the gate off (globally, or for --account); --for 2h re-enables it by itself",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gp, err := o.gate()
			if err != nil {
				return err
			}
			var until time.Time
			if forDur < 0 {
				return fmt.Errorf("--for must be positive")
			}
			if forDur > 0 {
				until = now().Add(forDur)
			}
			if err := setSwitch(gp, o.account, true, until); err != nil {
				return err
			}
			return printStatus(cmd.OutOrStdout(), gp, now())
		},
	}
	off.Flags().DurationVar(&forDur, "for", 0, "switch off for this long, then enforce again (default: until `pace on`)")
	on := &cobra.Command{
		Use:   "on",
		Short: "Switch the gate back on (globally, or for --account)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gp, err := o.gate()
			if err != nil {
				return err
			}
			if err := setSwitch(gp, o.account, false, time.Time{}); err != nil {
				return err
			}
			return printStatus(cmd.OutOrStdout(), gp, now())
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the switches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gp, err := o.gate()
			if err != nil {
				return err
			}
			return printStatus(cmd.OutOrStdout(), gp, now())
		},
	}
	c.AddCommand(off, on, status)
	return c
}

func printStatus(w io.Writer, gatePath string, at time.Time) error {
	g, err := readGate(gatePath)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "gate: %s\n", describe(g.Global, at))
	labels := make([]string, 0, len(g.Accounts))
	for l := range g.Accounts {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		fmt.Fprintf(w, "account %s: %s\n", l, describe(g.Accounts[l], at))
	}
	return nil
}
