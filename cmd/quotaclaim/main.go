// Command quotaclaim is a Claude Code Stop / SubagentStop hook that REFUSES a
// reply asserting a usage-limit state (5 h, weekly, per model; French or
// English) unless a fresh `aiquota status` stamp backs it.
//
// Hook contract (https://code.claude.com/docs/en/hooks): the event JSON
// arrives on stdin with last_assistant_message (and transcript_path, used as
// a fallback); to refuse, print {"decision":"block","reason":...} on stdout
// and exit 0 — Claude is sent back to keep working with the reason. No output
// and exit 0 allows the stop.
//
// A claim that the limit IS hit / blocked / resets on a date needs a stamp
// whose verdict is "exhausted". Any other mention of a limit needs a fresh
// stamp whose verdict is known (available or exhausted); an "unknown" stamp
// backs nothing.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-aiquota/tray/stamp"
)

var osExit = os.Exit

var now = time.Now

// hookInput is the subset of the Stop / SubagentStop event used here.
type hookInput struct {
	TranscriptPath       string `json:"transcript_path"`
	LastAssistantMessage string `json:"last_assistant_message"`
	StopHookActive       bool   `json:"stop_hook_active"`
}

// Claims about an EXHAUSTED state: they need an "exhausted" stamp.
var exhaustedRes = compile(
	`hit (your|the|my|its) [\w -]{0,20}limit`,
	`(reached|exceeded|used up|ran out of) (your|the|my|its) [\w -]{0,20}(limit|quota)`,
	`(weekly|5[- ]?h(our)?|session|usage|rate)[- ]limit(ed)?\s*(reached|hit|exceeded|exhausted)`,
	`limit (reached|exceeded|hit)`,
	`blocked (until|till)`,
	`bloqu\w+ jusqu`,
	`limite [\w' -]{0,30}(atteinte|épuisée|dépassée)`,
	`(atteint|épuisé|dépassé)\w* (la|votre|ta|ma) limite`,
	`quota (épuisé|exhausted|exceeded|dépassé)`,
)

// Any mention of a limit state: needs a fresh, known stamp.
var generalRes = compile(
	`weekly limit`, `usage limit`, `5[- ]?h(our)? limit`, `rate limit`,
	`limite hebdomadaire`, `limite de 5 ?h`, `limite d'usage`, `limite de session`,
	`resets? (on |at |in )?(mon|tue|wed|thu|fri|sat|sun|jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec|\d)`,
	`réinitialis\w+ (le|à|dans)`,
	`quota\W+(?:\w+\W+){0,6}(left|remaining|exhausted|reached|hit|restant|épuisé|atteint|dépassé|\d+ ?%)`,
	`(left|remaining|exhausted|reached|restant|épuisé|atteint|dépassé|\d+ ?%)\W+(?:\w+\W+){0,6}quota`,
)

func compile(pats ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(pats))
	for i, p := range pats {
		out[i] = regexp.MustCompile(`(?i)\b` + p)
	}
	return out
}

func anyMatch(res []*regexp.Regexp, s string) (string, bool) {
	for _, re := range res {
		if m := re.FindString(s); m != "" {
			return m, true
		}
	}
	return "", false
}

// decide returns "" to allow, else the refusal reason.
func decide(msg string, stampPath string, maxAge time.Duration, at time.Time) string {
	if hit, ok := anyMatch(exhaustedRes, msg); ok {
		return check(hit, stamp.Exhausted, stampPath, maxAge, at)
	}
	if hit, ok := anyMatch(generalRes, msg); ok {
		return check(hit, "", stampPath, maxAge, at)
	}
	return ""
}

func check(hit string, need stamp.Verdict, path string, maxAge time.Duration, at time.Time) string {
	remedy := "Run `aiquota status` (it measures every window live and stamps the result), then restate the limit from ITS output — or drop the claim. Never state a usage limit from memory or from an old error message."
	prefix := fmt.Sprintf("Blocked by quotaclaim: your reply asserts a usage-limit state (%q) ", hit)
	st, err := stamp.Read(path)
	switch {
	case err == stamp.ErrNoStamp:
		return prefix + "with no measurement on record. " + remedy
	case err != nil:
		return prefix + "and the stamp is unreadable (" + err.Error() + "). " + remedy
	}
	if age := st.Age(at); age > maxAge {
		return prefix + fmt.Sprintf("but the last measurement is %s old (limit %s). ", age.Round(time.Second), maxAge) + remedy
	}
	if st.Verdict == stamp.Unknown || st.Verdict == "" {
		return prefix + "but the last measurement could not read the limit (verdict UNKNOWN), which backs neither \"available\" nor \"exhausted\". Fix the reading (`aiquota status`) or say the state is unknown."
	}
	if need != "" && st.Verdict != need {
		return prefix + fmt.Sprintf("claiming it is %s, but the fresh measurement (%s) says %s. Restate from `aiquota status`.", need, st.At.Local().Format("15:04"), st.Verdict)
	}
	return ""
}

// lastAssistantText extracts the final assistant text from a transcript
// (JSON lines), the fallback when the event carries no last_assistant_message.
func lastAssistantText(r io.Reader) string {
	var last string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var line struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil || (line.Type != "assistant" && line.Message.Role != "assistant") {
			continue
		}
		var s string
		if json.Unmarshal(line.Message.Content, &s) == nil {
			last = s
			continue
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(line.Message.Content, &parts) == nil {
			var b strings.Builder
			for _, p := range parts {
				if p.Type == "text" {
					b.WriteString(p.Text + "\n")
				}
			}
			if b.Len() > 0 {
				last = b.String()
			}
		}
	}
	return last
}

func main() { osExit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, in io.Reader, out, errw io.Writer) int {
	code := 0
	cmd := RootCmd(in, &code)
	cmd.SetArgs(args)
	cmd.SetOut(out)
	cmd.SetErr(errw)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(errw, "quotaclaim:", err)
		return 1 // non-blocking error per the hooks contract: never wedge a session on our own bug
	}
	return code
}

// RootCmd builds the hook command.
func RootCmd(in io.Reader, code *int) *cobra.Command {
	var stampPath string
	var maxAge time.Duration
	c := &cobra.Command{
		Use:           "quotaclaim",
		Short:         "Stop hook: refuse an unmeasured usage-limit claim",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ev hookInput
			if err := json.NewDecoder(in).Decode(&ev); err != nil {
				return fmt.Errorf("reading hook JSON: %w", err)
			}
			msg := ev.LastAssistantMessage
			if msg == "" && ev.TranscriptPath != "" {
				if f, err := os.Open(ev.TranscriptPath); err == nil {
					msg = lastAssistantText(f)
					_ = f.Close()
				}
			}
			path := stampPath
			if path == "" {
				p, err := stamp.DefaultPath()
				if err != nil {
					return err
				}
				path = p
			}
			if reason := decide(msg, path, maxAge, now()); reason != "" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"decision": "block", "reason": reason})
			}
			return nil
		},
	}
	c.Flags().StringVar(&stampPath, "stamp", "", "stamp file (default os.UserConfigDir()/aiquota/last-status.json)")
	c.Flags().DurationVar(&maxAge, "max-age", 10*time.Minute, "how old a measurement may be")
	return c
}
