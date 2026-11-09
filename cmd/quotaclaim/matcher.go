// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"regexp"
	"strings"
)

// class is what a reply asserts about CLAUDE's OWN usage limits.
type class int

const (
	classNone class = iota
	classGeneral
	classExhausted
)

// The matcher is deliberately narrow: this machine's main project is an LLM
// gateway whose whole domain is quotas ("soft quota", rate_limit_exceeded,
// Retry-After, budgets). A bare "quota" or "limit" is NEVER a claim; only a
// phrase that names Claude's own windows or plan is, and a sentence that is
// plainly about a gateway / tenant / API budget is exempt unless it also
// addresses the reader's own usage ("you've hit your ...").
//
// Everything here is judged per SENTENCE, so a limit mentioned in one
// sentence cannot be blamed on a reset date in another.

var sentenceSplit = regexp.MustCompile(`[.!?;]\s+|\n+|\s·\s`)

// own: words that tie a sentence to a usage window of the user's Claude plan.
const ownWord = `(weekly|hebdo\w*|5[- ]?h(ours?)?|session limit|usage limit|hit your|limite|opus|sonnet|haiku)`

var exhaustedRes = compile(
	`(hit|reached|exceeded|used up) (your|my|our|the) ([\w-]+ ){0,2}(usage|weekly|5[- ]?hours?|session|opus|sonnet|claude)( [\w-]+)? limit`,
	`(weekly|5[- ]?hours?|session|usage|opus|sonnet) limit (reached|hit|exceeded)`,
	`blocked (until|till)\b`,
	`bloqu[^ ]* jusqu`,
	`limite hebdomadaire [\w' ]{0,20}(atteinte|épuisée|dépassée)`,
	`limite de 5 ?h [\w' ]{0,20}(atteinte|épuisée|dépassée)`,
	`(atteint|épuisé|dépassé)\w* (la|votre|ta|ma) limite (hebdomadaire|de 5 ?h|d'usage)`,
	`quota claude (est |a été )?(épuisé|exhausted|exceeded|atteint|dépassé)`,
)

var generalRes = compile(
	`weekly (usage )?limit`,
	`limite hebdomadaire`,
	`5[- ]?hours? (limit|session|window)`,
	`limite (de|des) 5 ?h`,
	`usage limit`,
	`limite d'usage`,
	`(opus|sonnet|haiku)( \d[\d.]*)? (usage |weekly )?limit`,
	`quota claude`,
	`claude('s)? (max |team |pro )?(plan )?(quota|usage limit)`,
	`resets? (on |at |in )?(mon|tue|wed|thu|fri|sat|sun|jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec|\d)`,
	`réinitialis\w+ (le|à|dans)`,
)

// blockedContext: "blocked until <date>" is a claim only next to a limit word
// ("blocked until the PR is merged" is not).
var blockedContext = regexp.MustCompile(`(?i)\b(limit|limite|quota|usage|weekly|hebdo\w*|5[- ]?h|reset|réinitial\w*|plan|abonnement|subagent|sous-agent)`)

// resetNeedsOwn: a reset date counts only beside one of the own-usage words.
var resetNeedsOwn = regexp.MustCompile(`(?i)\b` + ownWord)

// domain: gateway / API vocabulary. A sentence with one of these is exempt
// unless an anchor shows it addresses the user's own Claude usage.
var domain = regexp.MustCompile(`(?i)(gateway|passerelle|tenant|budget|établissement|etablissement|api[ _-]?key|clé api|retry-after|\b429\b|rate[_ -]?limit|token bucket|soft quota|hard quota|quota report|rapport de quota|max_tokens|github|litellm|upstream|proxy|per[- ]key|par clé|requests? per|\brpm\b|\btpm\b|llm|provider|fournisseur|\bvllm\b|\bollama\b|bucket|throttl)`)

// anchor: the sentence talks about the reader's own plan / session.
var anchor = regexp.MustCompile(`(?i)(\byour (weekly|5|usage|session|opus|sonnet|limit|plan)|you'?ve (hit|reached|exceeded)|\bmy (weekly|5|usage)|\bma limite|vous avez (atteint|dépassé)|claude (max|team|pro|code) |plan max|subagent|sous-agent|\bmon (plan|abonnement))`)

func compile(pats ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(pats))
	for i, p := range pats {
		// \b is ASCII-only in RE2: "épuisé" has no boundary after a space.
		out[i] = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}_])` + p)
	}
	return out
}

func firstMatch(res []*regexp.Regexp, s string) (string, bool) {
	for _, re := range res {
		if m := strings.TrimSpace(re.FindString(s)); m != "" {
			return strings.TrimLeft(m, "\"'([,:-"), true
		}
	}
	return "", false
}

// classify returns the strongest claim any sentence of msg makes, and the
// phrase that triggered it.
func classify(msg string) (class, string, string) {
	best, hit, sent := classNone, "", ""
	if harnessMessage.MatchString(msg) && !proseInHarness.MatchString(msg) { // the WHOLE message is the harness line
		return best, hit, sent
	}
	for _, s := range sentenceSplit.Split(msg, -1) {
		c, h := classifySentence(s)
		if c > best {
			best, hit, sent = c, h, s
		}
	}
	return best, hit, sent
}

// negated: "(not your usage limit)", "isn't a weekly limit": a denial, not a claim.
var negated = regexp.MustCompile(`(?i)\b(not|isn'?t|aren'?t|wasn'?t|never|n'est pas|pas)\b[^.\n]{0,20}\b(your|a|the|an|votre|la|une|de) ([\w-]+ ){0,2}(usage|weekly|5[- ]?hours?|session|hebdomadaire|d'usage)( [\w-]+)? (limit|limite)`)

// harnessMessage is the line Claude Code itself records when the account's
// limit stops a request ("You've hit your session limit · resets 7pm"). The
// harness measured it; the model did not assert it from memory.
var harnessMessage = regexp.MustCompile(`(?i)^\s*you'?ve (hit|reached) your [\w -]{1,30} limit(\s[·-]\sresets\s[^\n]{1,300})?\s*$`)

// proseInHarness: words a model adds when it QUOTES the harness line inside
// its own sentence ("... resets Nov 12, so I am blocked until Nov 12").
var proseInHarness = regexp.MustCompile(`(?i)\b(so|because|blocked|bloqu\w*|until|jusqu\w*|donc|waiting|wait)\b`)

// negatedFR is the French order: "n'est pas la limite hebdomadaire".
var negatedFR = regexp.MustCompile(`(?i)(n'est pas|\bpas)\b[^.\n]{0,20}limite (hebdomadaire|d'usage|de 5 ?h)`)

// pastMarker places a limit in the past ("earlier", "before the reset");
// presentMarker keeps it a claim about now ("still", "until", "can't").
var (
	pastMarker    = regexp.MustCompile(`(?i)\b(earlier|previously|before (the|it|they) reset|had (hit|reached)|this (afternoon|morning)|plus tôt|précédemment|avant (la|sa) réinitialisation|avai(en)?t (atteint|dépassé))\b`)
	presentMarker = regexp.MustCompile(`(?i)\b(still|now|until|till|again|blocked|cannot|can'?t|encore|toujours|maintenant|jusqu\w*|bloqu\w*)\b`)
)

func classifySentence(s string) (class, string) {
	if negated.MatchString(s) || negatedFR.MatchString(s) {
		return classNone, ""
	}
	if domain.MatchString(s) && !anchor.MatchString(s) {
		return classNone, ""
	}
	if h, ok := firstMatch(exhaustedRes, s); ok {
		if (strings.HasPrefix(strings.ToLower(h), "blocked") || strings.HasPrefix(strings.ToLower(h), "bloqu")) && !blockedContext.MatchString(s) {
			// "blocked until the PR merges": not a usage claim.
		} else if pastMarker.MatchString(s) && !presentMarker.MatchString(s) {
			// "They stopped earlier when they hit the 5-hour limit": a past
			// event, not today's state. Still a claim about the limit, so it
			// needs a fresh measurement, but one saying AVAILABLE backs it.
			return classGeneral, h
		} else {
			return classExhausted, h
		}
	}
	if h, ok := firstMatch(generalRes, s); ok {
		if strings.HasPrefix(strings.ToLower(h), "reset") || strings.HasPrefix(strings.ToLower(h), "réinit") {
			if !resetNeedsOwn.MatchString(strings.Replace(s, h, "", 1)) {
				return classNone, ""
			}
		}
		return classGeneral, h
	}
	return classNone, ""
}

// unmeasured: the reply itself says the state is unknown / not measured. Such
// a sentence asserts nothing and must not be blocked for lacking a reading.
var unmeasured = regexp.MustCompile(`(?i)\b(unknown|not measured|unmeasured|haven'?t measured|have not measured|could not (read|measure)|couldn'?t (read|measure)|cannot (read|measure)|inconnu\w*|non mesuré\w*|pas (été )?mesuré\w*|n'ai pas (pu )?mesurer|impossible de (lire|mesurer))`)
