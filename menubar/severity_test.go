// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"errors"
	"testing"

	"github.com/go-aiquota/proto/quotapb"
)

func snapshot(windows ...*quotapb.QuotaWindow) *quotapb.QuotaSnapshot {
	return &quotapb.QuotaSnapshot{Windows: windows}
}

func window(used, limit float64) *quotapb.QuotaWindow {
	return &quotapb.QuotaWindow{Used: used, Limit: limit}
}

func TestWindowSeverityThresholds(t *testing.T) {
	cases := []struct {
		name string
		w    *quotapb.QuotaWindow
		want Severity
	}{
		{"comfortably under", window(10, 100), SeverityOK},
		{"just under warning", window(69, 100), SeverityOK},
		{"at warning threshold", window(70, 100), SeverityWarning},
		{"between warning and critical", window(80, 100), SeverityWarning},
		{"at critical threshold", window(90, 100), SeverityCritical},
		{"over limit", window(105, 100), SeverityCritical},
		{"zero limit is unknown, not a divide-by-zero OK", window(0, 0), SeverityUnknown},
		{"negative limit is unknown", window(5, -1), SeverityUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := windowSeverity(c.w, DefaultThresholds); got != c.want {
				t.Errorf("windowSeverity(%+v) = %v, want %v", c.w, got, c.want)
			}
		})
	}
}

func TestAccountStatusSeverity(t *testing.T) {
	cases := []struct {
		name string
		s    AccountStatus
		want Severity
	}{
		{"no poll yet", AccountStatus{}, SeverityUnknown},
		{"failed poll beats any prior snapshot state", AccountStatus{Err: errors.New("boom"), Snapshot: snapshot(window(1, 100))}, SeverityError},
		{"ok snapshot, one window", AccountStatus{Snapshot: snapshot(window(10, 100))}, SeverityOK},
		{"worst of several windows wins", AccountStatus{Snapshot: snapshot(window(10, 100), window(95, 100))}, SeverityCritical},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.s.Severity(DefaultThresholds); got != c.want {
				t.Errorf("Severity() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAggregateIsTheWorstAcrossAccounts(t *testing.T) {
	accounts := []AccountStatus{
		{AccountID: "a", Snapshot: snapshot(window(10, 100))}, // OK
		{AccountID: "b", Snapshot: snapshot(window(75, 100))}, // Warning
		{AccountID: "c", Err: errors.New("session expired")},  // Error
	}
	if got := Aggregate(accounts, DefaultThresholds); got != SeverityError {
		t.Fatalf("Aggregate = %v, want SeverityError (the worst of OK/Warning/Error)", got)
	}
}

func TestAggregateEmptyIsUnknown(t *testing.T) {
	if got := Aggregate(nil, DefaultThresholds); got != SeverityUnknown {
		t.Fatalf("Aggregate(nil) = %v, want SeverityUnknown", got)
	}
}

func TestAggregateCriticalBeatsWarningButNotError(t *testing.T) {
	accounts := []AccountStatus{
		{Snapshot: snapshot(window(95, 100))}, // Critical
		{Snapshot: snapshot(window(75, 100))}, // Warning
	}
	if got := Aggregate(accounts, DefaultThresholds); got != SeverityCritical {
		t.Fatalf("Aggregate = %v, want SeverityCritical", got)
	}
}
