// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/go-aiquota/tray/stamp"
)

// Negatives: gateway / API / budget vocabulary and ordinary engineering text.
// None of these is a claim about Claude's own usage limits.
var negatives = []string{
	"The soft quota is 80% of the hard quota; above the soft quota we only warn.",
	"Hard quota exceeded for tenant acme: the gateway returns 429.",
	"The upstream returned rate_limit_exceeded; we honour Retry-After and back off.",
	"Retry-After: 30 on the 429 response, then the client retries.",
	"Generate the quota report per team and attach it to the monthly review.",
	"Le quota de l'établissement est atteint, la passerelle refuse les nouvelles requêtes.",
	"Le quota de l'établissement est de 2 millions de tokens par mois.",
	"The budget limit for this API key is 50 EUR per month.",
	"Budget limit reached: the key is disabled until the next billing period.",
	"The request failed because max_tokens limit was lower than the prompt.",
	"max_tokens limit is 4096 for this model; raise it in the config.",
	"GitHub API rate limit: 5000 requests per hour, resets at 14:00 UTC.",
	"gh api reports the rate limit remaining as 4210; the core limit resets at 15:12.",
	"The secondary rate limit of GitHub blocked us for a minute.",
	"We hit the rate limit of the provider and the proxy queued the call.",
	"Per-key quota: 100 requests per minute, enforced by the token bucket.",
	"The quota middleware reads the counter from Redis and compares it with the limit.",
	"Quota exhausted on the vLLM backend, the router fails over to Ollama.",
	"Le quota par clé API est dépassé, le gateway renvoie une 429.",
	"tenant weekly limit of 10M tokens is enforced by the budget service.",
	"The usage limit per tenant is stored in the budget table.",
	"Limit the number of retries to three and log each attempt.",
	"The cache resets to zero on Close.",
	"The counter resets at midnight UTC in the quota service.",
	"Daily quota resets at 00:00, so the dashboard shows 0% at that time.",
	"Pas de limite de taille pour ce champ.",
	"The memory limit of the container is 512 MiB.",
	"Disk quota on the volume is 20 GiB.",
	"Set a soft limit and a hard limit in ulimit -v.",
	"The litellm proxy applies a tpm limit and an rpm limit per deployment.",
	"Blocked until the PR is merged and CI is green.",
	"Bloqué jusqu'à la fusion de la PR.",
	"The token bucket resets every 60 seconds.",
	"API Error: Server is temporarily limiting requests (not your usage limit)",
	"This is not a weekly limit, it is a transient 529 overloaded error.",
	"Ce n'est pas la limite hebdomadaire, c'est une erreur réseau.",
	"The rate-limited request is retried by the gateway client.",
	"Quota check: 3 of 5 seats used.",
	"The weekly job runs on Monday; its limit of 100 rows is configurable.",
	"",
}

type pos struct {
	msg string
	c   class
}

// Positives: claims about Claude's own windows or plan.
var positives = []pos{
	{"You've hit your weekly limit and it resets Nov 12, so I am blocked until Nov 12.", classExhausted},
	{"The subagent died: You've hit your weekly limit · resets Nov 12", classExhausted},
	{"I have hit your 5-hour limit; it resets at 14:00.", classExhausted},
	{"Je suis bloqué jusqu'au 12 novembre : limite hebdomadaire atteinte.", classExhausted},
	{"Bloqué jusqu'au 12 novembre à cause de la limite hebdomadaire.", classExhausted},
	{"We are blocked until Nov 12 because of the weekly limit.", classExhausted},
	{"The agent hit the usage limit again.", classExhausted},
	{"Sonnet usage limit reached.", classExhausted},
	{"Claude weekly limit reached, resets Nov 12.", classExhausted},
	{"La limite de 5 h est atteinte, réinitialisation à 19h.", classExhausted},
	{"Le quota Claude est épuisé jusqu'à vendredi.", classExhausted},
	{"The 5-hour session resets at 14:00.", classGeneral},
	{"Your weekly limit resets Nov 12.", classGeneral},
	{"Il reste de la marge sur la limite hebdomadaire.", classGeneral},
	{"The Opus limit resets Tuesday at 9am.", classGeneral},
	{"Sonnet 5.5 usage limit is at 40%.", classGeneral},
	{"Both agents stopped on the 5-hour usage limit last night.", classGeneral},
	{"Le quota Claude Max est à 40 %.", classGeneral},
	{"Claude's usage limit is not a problem today.", classGeneral},
	// French relays (incident 2026-11-26, go-vcs).
	{"Deux agents sont arrêtés par la limite des 5 heures de Sonnet jusqu'à **19:30**. Je ne peux pas les relancer avant 19:30.", classExhausted},
	{"Deux agents sont arrêtés par la limite des 5 heures de Sonnet jusqu'à 19:30", classExhausted},
	{"Les agents sont bloqués jusqu'à 19:30 par la limite de 5 heures.", classExhausted},
	{"La limite de 5 heures est atteinte.", classExhausted},
	{"La limite hebdomadaire est atteinte pour Sonnet.", classExhausted},
	{"Le quota Sonnet est épuisé.", classExhausted},
	{"La limite de 5 heures de Sonnet est là. Je ne peux pas relancer avant 19:30.", classExhausted},
	{"La limite de Sonnet est là, l'agent reprendra à 19h30.", classExhausted},
}

// Negatives specific to the French table.
var negativesFR = []string{
	"Nous avons une limite de 64 dépôts par organisation.",
	"La limite de 5 hôtes est atteinte dans le cluster.",
	"Le build reprend à 14h après la fusion.",
	"Je ne peux pas relancer avant 19:30 car le serveur de CI redémarre.",
	"Le quota de disque est atteint sur le volume.",
	"Bloqué jusqu'à 18:00 par la revue de code.",
}

func TestMatcherNegativesFR(t *testing.T) {
	for _, m := range negativesFR {
		if c, hit, _ := classify(m); c != classNone {
			t.Errorf("false positive %q on %q", hit, m)
		}
	}
}

// The French incident sentence must be REFUSED when a fresh measurement says
// AVAILABLE, and allowed when the fresh measurement says EXHAUSTED.
func TestFrenchRelayRefusedWhenAvailable(t *testing.T) {
	msg := "Deux agents sont arrêtés par la limite des 5 heures de Sonnet jusqu'à **19:30**. Je ne peux pas les relancer avant 19:30."
	if c, _, _ := classify(msg); c != classExhausted {
		t.Fatalf("class %d, want exhausted", c)
	}
	if code, out := hook(t, msg, put(t, stamp.Available, time.Minute)); code != 0 || !strings.Contains(out, "says available") {
		t.Errorf("AVAILABLE stamp must refuse the French relay: code %d out %q", code, out)
	}
	if _, out := hook(t, msg, put(t, stamp.Exhausted, time.Minute)); out != "" {
		t.Errorf("EXHAUSTED fresh stamp backs the claim, got %q", out)
	}
}

func FuzzClassify(f *testing.F) {
	for _, p := range positives {
		f.Add(p.msg)
	}
	for _, n := range negativesFR {
		f.Add(n)
	}
	f.Fuzz(func(t *testing.T, s string) {
		classify(s) // must not panic or hang
	})
}

func TestMatcherNegatives(t *testing.T) {
	if len(negatives) < 30 {
		t.Fatalf("need at least 30 negatives, have %d", len(negatives))
	}
	for _, m := range negatives {
		if c, hit, _ := classify(m); c != classNone {
			t.Errorf("false positive %q on %q", hit, m)
		}
	}
}

func TestMatcherPositives(t *testing.T) {
	for _, p := range positives {
		c, hit, _ := classify(p.msg)
		if c == classNone {
			t.Errorf("missed: %q", p.msg)
		} else if c != p.c {
			t.Errorf("%q: class %d (%q), want %d", p.msg, c, hit, p.c)
		}
	}
}

func TestHarnessLineIsNotTheModelsClaim(t *testing.T) {
	if c, _, _ := classify("You've hit your session limit · resets 7pm"); c != classNone {
		t.Error("the harness's own line, alone, must not be gated")
	}
}

func TestUnmeasuredHedge(t *testing.T) {
	for _, s := range []string{
		"The weekly limit state is unknown: I have not measured it.",
		"Limite hebdomadaire : état non mesuré.",
	} {
		_, _, sent := classify(s)
		if !unmeasured.MatchString(sent) {
			t.Errorf("hedge not recognised in %q (sentence %q)", s, sent)
		}
	}
}
