// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"fmt"
	"log"
	"time"

	"github.com/go-aiquota/tray/history"
	"github.com/go-aiquota/tray/menubar"
	"github.com/go-widgets/toolkit"
	"github.com/go-widgets/window"
)

const (
	historyWindowWidth  = 400
	historyWindowHeight = 220
)

// historySeries are the logical series (see menubar.ClassifyWindow) a
// history window ever charts.
var historySeries = []string{"session", "weekly"}

// openHistoryWindow renders accountLabel's recent session/weekly history
// from store into a small native window and blocks until it's closed —
// the same open/run/close shape onboarding's window uses, but a plain
// drawn chart rather than a live browser page. Dispatched through
// app.Loop like every other action (see app.Handlers.OnViewHistory), so
// it already runs with the tray's own Hold released, exactly as
// addAccount's onboarding window does.
func openHistoryWindow(store *history.Store, accountID, accountLabel string) error {
	series := historySeriesFor(store, accountID)
	pixels, err := menubar.RenderChart(historyWindowWidth, historyWindowHeight, series, time.Now())
	if err != nil {
		return fmt.Errorf("rendering the history chart: %w", err)
	}
	win, err := window.Open(window.Config{
		Title:  accountLabel + " — usage history",
		Width:  historyWindowWidth,
		Height: historyWindowHeight,
	})
	if err != nil {
		return fmt.Errorf("opening the history window: %w", err)
	}
	defer win.Close()
	return win.Run(toolkit.NewImage(pixels, historyWindowWidth, historyWindowHeight))
}

// historySeriesFor reads accountID's session/weekly points back out of
// store, going back as far as the store retains (history.Retention) — an
// empty or read-erroring series is simply omitted rather than failing the
// whole window (RenderChart already draws a blank panel for a series with
// nothing in it).
func historySeriesFor(store *history.Store, accountID string) map[string][]history.Point {
	since := time.Now().Add(-history.Retention)
	series := map[string][]history.Point{}
	for _, key := range historySeries {
		pts, err := store.Recent(accountID, key, since)
		if err != nil {
			log.Printf("aiquota-tray: reading %s history for %s: %v", key, accountID, err)
			continue
		}
		if len(pts) > 0 {
			series[key] = pts
		}
	}
	return series
}
