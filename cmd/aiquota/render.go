// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

// The generic render description (--render 1). The VS Code extension is a
// frozen renderer of it: it draws exactly this list of items and nothing
// else, so the number of items, their order, labels, colours, bands, click
// actions and the poll interval are all decided here, and a changed look is a
// `go install` of a new aiquota, never a reload of the editor. The binary
// keeps emitting what render 1 understands; a later render 2 is asked for
// explicitly.

const renderV1 = 1

// Click actions a render 1 shell implements.
const (
	actionDetail  = "detail"  // show the render's detail text
	actionRefresh = "refresh" // measure again now
	actionNone    = "none"
)

// defaultPollSeconds is the interval the shell waits between measurements
// unless the user set one; the shell never goes below 30 s.
const defaultPollSeconds = 60

// itemStep is the priority offset between two items; they all lie inside
// (-1, 0] so no other extension's integer priority falls between them.
const itemStep = 0.001

type renderItem struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Tooltip    string  `json:"tooltip"`
	Color      string  `json:"color"`      // #RRGGBB or a theme colour id
	Background string  `json:"background"` // none, warning or error
	Priority   float64 `json:"priority"`   // offset from the user's base priority
	Alignment  string  `json:"alignment"`  // right or left
	Command    string  `json:"command"`    // detail, refresh or none
}

type renderDoc struct {
	Render      int          `json:"render"`
	Items       []renderItem `json:"items"`
	Detail      string       `json:"detail"`
	PollSeconds int          `json:"poll_seconds"`
	Level       string       `json:"level"`
	Verdict     string       `json:"verdict"`
}

// buildRender turns the status bar into the generic description.
func buildRender(sb statusBar) renderDoc {
	d := renderDoc{Render: renderV1, Detail: sb.Tooltip, PollSeconds: defaultPollSeconds, Level: sb.Level, Verdict: sb.Verdict}
	for i, it := range sb.Items {
		d.Items = append(d.Items, renderItem{
			ID: it.ID, Text: it.Text, Tooltip: sb.Tooltip, Color: it.Color, Background: it.Background,
			Priority: -float64(i) * itemStep, Alignment: "right", Command: actionDetail,
		})
	}
	return d
}
