// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import "regexp"

// vocabRule is one row of a phrase table. needsOwn: the phrase is a claim only
// when the MESSAGE also names a usage window or model (ownWord), because on
// its own it is ordinary prose ("le build reprend à 14h").
type vocabRule struct {
	name     string
	pattern  string
	needsOwn bool
}

const clock = `\d{1,2}([h:]\d{2}|h)?`

// exhaustedFR: French relays of "Claude's own limit stops me until HH:MM".
// Add a row here (and a case in matcher_test.go), not another ad-hoc regex.
var exhaustedFR = []vocabRule{
	{"stopped-by-limit", `arrêt\p{L}* par (la|le|les) (limite|quota)`, false},
	{"limit-5h-reached", `limite (de|des) 5 ?h(eures?)?([^\p{L}]|$)[\w' ]{0,25}(atteinte|épuisée|dépassée)`, false},
	{"weekly-limit-reached", `limite hebdomadaire [\w' ]{0,25}(atteinte|épuisée|dépassée)`, false},
	{"quota-exhausted", `quota( \p{L}+)? (est |a été )?(épuisé|atteint)`, true},
	{"blocked-until-clock", `bloqu\p{L}* jusqu['’]?(à|au) ` + clock, true},
	{"cannot-relaunch-before", `ne (peux|peut|pouvons) pas (les |le |la )?relancer avant ` + clock, true},
	{"resumes-at", `reprend(ra|ront)? à ` + clock, true},
}

type compiledRule struct {
	name     string
	re       *regexp.Regexp
	needsOwn bool
}

var exhaustedFRCompiled = compileRules(exhaustedFR)

func compileRules(rs []vocabRule) []compiledRule {
	out := make([]compiledRule, len(rs))
	for i, r := range rs {
		out[i] = compiledRule{r.name, compile(r.pattern)[0], r.needsOwn}
	}
	return out
}

// firstRule returns the first table row matching s (own: the message names a
// usage window or model).
func firstRule(rs []compiledRule, s string, own bool) (string, bool) {
	for _, r := range rs {
		if r.needsOwn && !own {
			continue
		}
		if h, ok := firstMatch([]*regexp.Regexp{r.re}, s); ok {
			return h, true
		}
	}
	return "", false
}
