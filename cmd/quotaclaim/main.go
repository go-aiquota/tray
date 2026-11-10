// Command quotaclaim is a Claude Code Stop / SubagentStop hook that REFUSES a
// reply asserting a state of CLAUDE's OWN usage limits (5 h, weekly, per
// model; French or English) unless a fresh `aiquota status` stamp backs it.
//
// Hook contract (https://code.claude.com/docs/en/hooks): the event JSON
// arrives on stdin with last_assistant_message (and transcript_path, used as
// a fallback); to refuse, print {"decision":"block","reason":...} on stdout
// and exit 0 — Claude is sent back to keep working with the reason. No output
// and exit 0 allows the stop.
//
// A claim that the limit IS hit / blocked needs a fresh stamp whose verdict is
// "exhausted"; any other claim needs a fresh stamp with a known verdict. With
// no usable measurement (none, stale, UNKNOWN) a claim passes only if the
// claiming sentence itself says "unknown" / "not measured".
//
// The matcher (matcher.go) ignores gateway vocabulary — soft/hard quota,
// rate_limit_exceeded, Retry-After, budgets — see its table test.
//
//	quotaclaim scan [--dir ~/.claude/projects]   measure the matcher on real transcripts
package main

import (
	"bufio"
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

// hookInput is the subset of the Stop / SubagentStop event used here.
type hookInput struct {
	TranscriptPath       string `json:"transcript_path"`
	LastAssistantMessage string `json:"last_assistant_message"`
	StopHookActive       bool   `json:"stop_hook_active"`
}

// decide returns "" to allow, else the refusal reason.
func decide(msg string, stampPath string, maxAge time.Duration, at time.Time) string {
	c, hit, sentence := classify(msg)
	if c == classNone {
		return ""
	}
	remedy := "Run `aiquota status` (it measures every window live and stamps the result), then restate the limit from ITS output — or say plainly that the state is unknown / not measured. Never state a usage limit from memory or from an old error message."
	prefix := fmt.Sprintf("Blocked by quotaclaim: your reply asserts a usage-limit state (%q) ", hit)

	st, err := stamp.Read(path(stampPath))
	known := err == nil && st.Age(at) <= maxAge && (st.Verdict == stamp.Available || st.Verdict == stamp.Exhausted)
	if known {
		if c == classExhausted && st.Verdict != stamp.Exhausted {
			return prefix + fmt.Sprintf("claiming it is exhausted, but the fresh measurement (%s) says %s. Restate from `aiquota status`.", st.At.Local().Format("15:04"), st.Verdict)
		}
		return ""
	}
	if unmeasured.MatchString(sentence) {
		return "" // it asserts nothing: it says the state is not known
	}
	switch {
	case err == stamp.ErrNoStamp:
		return prefix + "with no measurement on record. " + remedy
	case err != nil:
		return prefix + "and the stamp is unreadable (" + err.Error() + "). " + remedy
	case st.Age(at) > maxAge:
		return prefix + fmt.Sprintf("but the last measurement is %s old (limit %s). ", st.Age(at).Round(time.Second), maxAge) + remedy
	}
	return prefix + "but the last measurement could not read the limit (verdict UNKNOWN), which backs neither \"available\" nor \"exhausted\". " + remedy
}

// path resolves the stamp path; an unresolvable default yields "" which Read
// reports as an error, so the claim is refused rather than waved through.
func path(p string) string {
	if p != "" {
		return p
	}
	d, err := stamp.DefaultPath()
	if err != nil {
		return ""
	}
	return d
}

// assistantTexts calls fn with the text of every assistant message of a
// transcript (JSON lines).
func assistantTexts(r io.Reader, fn func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
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
			fn(s)
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
				fn(b.String())
			}
		}
	}
}

// lastAssistantText is the fallback when the event carries no
// last_assistant_message.
func lastAssistantText(r io.Reader) string {
	var last string
	assistantTexts(r, func(s string) { last = s })
	return last
}

func main() { osExit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, in io.Reader, out, errw io.Writer) int {
	cmd := RootCmd(in)
	cmd.SetArgs(args)
	cmd.SetOut(out)
	cmd.SetErr(errw)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(errw, "quotaclaim:", err)
		return 1 // non-blocking error per the hooks contract: never wedge a session on our own bug
	}
	return 0
}

// RootCmd builds the hook command (and its scan subcommand).
func RootCmd(in io.Reader) *cobra.Command {
	var stampPath string
	var maxAge time.Duration
	c := &cobra.Command{
		Use:           "quotaclaim",
		Version:       buildver.Version(),
		Short:         "Stop hook: refuse an unmeasured claim about Claude's own usage limits",
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
			if ev.StopHookActive {
				// Already sent back once this turn: blocking again risks a loop
				// (Claude cannot always run the measurement, e.g. when the very
				// limit is hit). The first refusal has done its job.
				return nil
			}
			if reason := decide(msg, stampPath, maxAge, now()); reason != "" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"decision": "block", "reason": reason})
			}
			return nil
		},
	}
	c.Flags().StringVar(&stampPath, "stamp", "", "stamp file (default os.UserConfigDir()/aiquota/last-status.json)")
	c.Flags().DurationVar(&maxAge, "max-age", 10*time.Minute, "how old a measurement may be")
	c.SetVersionTemplate("quotaclaim {{.Version}}\n")
	c.AddCommand(scanCmd())
	return c
}
