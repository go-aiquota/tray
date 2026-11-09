// Command aiquota asks the Claude quota plugin, LIVE, for every usage window
// (5 h session, weekly, any per-model window) and prints used %, reset time
// (local, 24 h) and plan.
//
//	aiquota status [--account A] [-o json|text]
//
// Exit status: 0 available, 1 exhausted, 3 UNKNOWN (could not read), 2 usage
// error. Unknown is never reported as available or exhausted.
//
// Every run writes a receipt (os.UserConfigDir()/aiquota/last-status.json)
// that the quotaclaim Stop hook checks before a reply may state a limit.
//
// Credentials stay inside the keyring and the plugin's gRPC channel: nothing
// secret is read from argv or the environment, and every error printed here
// passes through proto's redactor.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/go-aiquota/proto/quotapb"
	"github.com/go-aiquota/proto/redact"
	"github.com/go-aiquota/tray/account"
	"github.com/go-aiquota/tray/quota"
	"github.com/go-aiquota/tray/stamp"
)

// Exit statuses.
const (
	exitAvailable = 0
	exitExhausted = 1
	exitUsage     = 2
	exitUnknown   = 3
)

var osExit = os.Exit

// now is replaceable so a test can pin the clock.
var now = time.Now

// backend is what status needs from the world: the accounts and one live
// snapshot per account. A test supplies a fake.
type backend interface {
	Accounts() ([]account.Account, error)
	Fetch(ctx context.Context, a account.Account) (*quotapb.QuotaSnapshot, error)
	Close()
}

// newBackend builds the real backend; replaced by tests.
var newBackend = func() (backend, error) {
	path, err := account.DefaultPath()
	if err != nil {
		return nil, err
	}
	return &realBackend{store: &account.Store{Path: path}, mgr: quota.NewManager(locateBinary)}, nil
}

type realBackend struct {
	store *account.Store
	mgr   *quota.Manager
}

func (b *realBackend) Accounts() ([]account.Account, error) { return b.store.List() }
func (b *realBackend) Close()                               { b.mgr.Close() }

// claudeCodeAccount is the virtual account for Claude Code's own login: the
// same account, plan and windows as the VS Code extension's Account & usage
// panel. It has no stored credential — the plugin reads Claude Code's own
// storage at fetch time, so nothing secret passes through here at all.
var claudeCodeAccount = account.Account{ID: "claude-code", Provider: "claude", Label: "Claude Code"}

func (b *realBackend) Fetch(ctx context.Context, a account.Account) (*quotapb.QuotaSnapshot, error) {
	if a.ID == claudeCodeAccount.ID {
		return b.mgr.FetchQuota(ctx, a.Provider, a.Label, map[string]string{"source": "claude-code"})
	}
	cred, err := b.store.Credential(a.ID)
	if err != nil {
		return nil, fmt.Errorf("credential: %w", err)
	}
	return b.mgr.FetchQuota(ctx, a.Provider, a.Label, cred)
}

func locateBinary(provider string) (string, error) {
	p, err := exec.LookPath(quota.BinaryPrefix + provider)
	if err != nil {
		return "", fmt.Errorf("no %s%s on PATH: %w", quota.BinaryPrefix, provider, err)
	}
	return p, nil
}

// codeError carries an exit status out of RunE without printing anything.
type codeError struct{ code int }

func (e codeError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

func main() { osExit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run executes the CLI and returns its exit status.
func run(args []string, out, errw io.Writer) int {
	cmd := RootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(out)
	cmd.SetErr(errw)
	err := cmd.Execute()
	var ce codeError
	switch {
	case err == nil:
		return exitAvailable
	case errors.As(err, &ce):
		return ce.code
	default:
		return exitUsage
	}
}

// RootCmd builds the command tree.
func RootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "aiquota",
		Short:         "Measure AI usage limits live",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(statusCmd())
	return root
}

func statusCmd() *cobra.Command {
	var (
		acct, format, provider, stampPath string
		debug                             bool
		warn, critical                    int
		timeout                           time.Duration
	)
	c := &cobra.Command{
		Use:   "status",
		Short: "Ask the provider plugin live for every window and record a stamp",
		Long: `Ask the provider plugin live for every usage window and print used %,
the reset time (local, 24 h) and the plan.

Exit status: 0 available, 1 exhausted, 3 UNKNOWN (could not read), 2 usage error.
A stamp is written to os.UserConfigDir()/aiquota/last-status.json (or --stamp)
for the quotaclaim hook, whatever the verdict.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "text" && format != "json" && format != "statusbar" {
				return fmt.Errorf("-o: want text, json or statusbar, got %q", format)
			}
			if warn < 0 || critical < warn || critical > 100 {
				return fmt.Errorf("--warn/--critical: want 0 <= warn <= critical <= 100, got %d/%d", warn, critical)
			}
			if debug {
				quota.PluginLogLevel = hclog.Debug
			}
			be, err := newBackend()
			var st stamp.Stamp
			if err != nil {
				st = unreadable(fmt.Sprintf("backend: %v", err))
			} else {
				defer be.Close()
				st = measure(cmd.Context(), be, provider, acct, timeout)
			}
			path := stampPath
			if path == "" {
				if path, err = stamp.DefaultPath(); err != nil {
					return err
				}
			}
			if err := stamp.Write(path, st); err != nil {
				// Without a receipt the gate cannot be satisfied: say so loudly.
				fmt.Fprintf(cmd.ErrOrStderr(), "aiquota: cannot write stamp: %s\n", clean(err))
			}
			if format == "statusbar" {
				err = renderStatusBar(cmd.OutOrStdout(), st, warn, critical)
			} else {
				err = render(cmd.OutOrStdout(), st, format)
			}
			if err != nil {
				return err
			}
			return codeError{code: exitCode(st.Verdict)}
		},
	}
	c.Flags().StringVar(&acct, "account", "", "only this account (id or label)")
	c.Flags().StringVarP(&format, "output", "o", "text", "output format: text, json, or statusbar (one JSON line: text, tooltip, level, for an editor status bar)")
	c.Flags().IntVar(&warn, "warn", 75, "statusbar: percent from which the level is warn")
	c.Flags().IntVar(&critical, "critical", 90, "statusbar: percent from which the level is error")
	c.Flags().StringVar(&provider, "provider", "claude", "provider plugin")
	c.Flags().StringVar(&stampPath, "stamp", "", "stamp file (default os.UserConfigDir()/aiquota/last-status.json)")
	c.Flags().BoolVar(&debug, "debug", false, "show the plugin client's DEBUG/TRACE log on stderr")
	c.Flags().DurationVar(&timeout, "timeout", 20*time.Second, "per-account timeout")
	return c
}

func exitCode(v stamp.Verdict) int {
	switch v {
	case stamp.Available:
		return exitAvailable
	case stamp.Exhausted:
		return exitExhausted
	}
	return exitUnknown
}

// clean is err's message with credential-shaped text removed; for a gRPC
// error only its description is kept ("rpc error: code = X desc = " is noise).
func clean(err error) string {
	msg := err.Error()
	if s, ok := status.FromError(err); ok {
		msg = s.Message()
	}
	return redact.New().String(msg)
}

func unreadable(problem string) stamp.Stamp {
	return stamp.Stamp{Version: stamp.Version, At: now().UTC(), Verdict: stamp.Unknown, Problems: []string{problem}}
}

// measure polls the matching accounts and builds the stamp. Any failure is a
// problem plus an Unknown window, never a silent omission.
func measure(ctx context.Context, be backend, provider, only string, timeout time.Duration) stamp.Stamp {
	st := stamp.Stamp{Version: stamp.Version, At: now().UTC()}

	// query records one account's windows (or its failure) in st and reports
	// whether the failure was "no such login" (gRPC NotFound).
	query := func(a account.Account) (notFound bool) {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		snap, err := fetchWithin(cctx, be, a)
		cancel()
		if err != nil || snap == nil {
			msg := "no snapshot"
			if err != nil {
				msg = clean(err)
			}
			if status.Code(err) == codes.NotFound && a.ID == claudeCodeAccount.ID {
				return true // nothing recorded: the caller decides whether to fall back
			}
			st.Problems = append(st.Problems, a.Label+": "+msg)
			st.Windows = append(st.Windows, stamp.Window{Account: a.Label, Label: "(unreadable)", Verdict: stamp.Unknown, Note: msg})
			return false
		}
		if snap.PlanKind != "" {
			st.Plans = append(st.Plans, a.Label+"="+snap.PlanKind)
		}
		for _, w := range snap.Windows {
			win := windowOf(a.Label, w)
			win.Plan = snap.PlanKind
			st.Windows = append(st.Windows, win)
		}
		for _, warn := range snap.Warnings {
			st.Problems = append(st.Problems, a.Label+": warning: "+warn)
		}
		if len(snap.Windows) == 0 {
			st.Windows = append(st.Windows, stamp.Window{Account: a.Label, Label: "(no window reported)", Verdict: stamp.Unknown})
		}
		return false
	}

	wantClaudeCode := only == "" || strings.EqualFold(only, claudeCodeAccount.ID) || strings.EqualFold(only, claudeCodeAccount.Label)
	if wantClaudeCode && provider == claudeCodeAccount.Provider {
		// Default source: Claude Code's own login, the same view as the VS
		// Code extension. Other accounts are the fallback, used only when no
		// Claude Code login exists (or when named with --account).
		if !query(claudeCodeAccount) || only != "" {
			st.Verdict = stamp.Overall(st.Windows)
			return st
		}
	}

	accts, err := be.Accounts()
	if err != nil {
		st.Verdict = stamp.Unknown
		st.Problems = append(st.Problems, "listing accounts: "+clean(err))
		return st
	}
	n := 0
	for _, a := range accts {
		if a.Provider != provider || (only != "" && a.ID != only && a.Label != only) {
			continue
		}
		n++
		query(a)
	}
	if n == 0 {
		st.Problems = append(st.Problems, "no Claude Code login and no matching "+provider+" account")
	}
	st.Verdict = stamp.Overall(st.Windows)
	return st
}

// fetchWithin enforces ctx's deadline even where the backend cannot: the
// keyring read (a presence prompt that may never be answered) takes no
// context. A hung read is UNKNOWN, never a hang, so the gate stays usable.
func fetchWithin(ctx context.Context, be backend, a account.Account) (*quotapb.QuotaSnapshot, error) {
	type result struct {
		snap *quotapb.QuotaSnapshot
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := be.Fetch(ctx, a)
		ch <- result{s, err}
	}()
	select {
	case r := <-ch:
		return r.snap, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("no answer within the timeout (a stored claude.ai account: most likely a macOS keychain dialog nobody answered, \"Always Allow\" once; the Claude Code source needs no dialog): %w", ctx.Err())
	}
}

func windowOf(acct string, w *quotapb.QuotaWindow) stamp.Window {
	out := stamp.Window{Account: acct, Label: w.Label, Used: w.Used, Limit: w.Limit, ResetsAtUnix: w.ResetsAtUnix}
	switch {
	case w.Limit <= 0:
		out.Verdict = stamp.Unknown
		out.Note = "provider gave no limit"
	case w.Used >= w.Limit:
		out.UsedPercent = 100 * w.Used / w.Limit
		out.Verdict = stamp.Exhausted
	default:
		out.UsedPercent = 100 * w.Used / w.Limit
		out.Verdict = stamp.Available
	}
	return out
}

// panelPercent is what the panel's bar prints: floor of the value clamped to
// 0..100 (webview dle: Math.floor(Math.min(100, Math.max(0, t)))).
func panelPercent(p float64) int {
	return int(math.Floor(math.Min(100, math.Max(0, p))))
}

// panelReset mirrors webview Zy(resetsAt, display, showDay): "soon" under a
// minute; "at HH:MM" for a window shown without its day (the 5-hour session)
// that resets within 24 h; otherwise "Ddd, Mon D at HH:MM" (with the year when
// it is not the current one). Times are local, 24 h.
func panelReset(unix int64, label string, at time.Time) string {
	t := time.Unix(unix, 0)
	remaining := t.Sub(at)
	if remaining < time.Minute {
		return "soon"
	}
	clock := t.Local().Format("15:04")
	showDay := !strings.HasPrefix(strings.ToLower(label), "session")
	if remaining < 24*time.Hour && !showDay {
		return "at " + clock
	}
	day := t.Local().Format("Mon, Jan 2")
	if t.Local().Year() != at.Local().Year() {
		day = t.Local().Format("Mon, Jan 2, 2006")
	}
	return day + " at " + clock
}

func render(w io.Writer, st stamp.Stamp, format string) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	// The same labels, order, whole percentages and reset wording as the VS
	// Code extension's "Account & usage" panel (webview/index.js: WDn, dle,
	// Zy), so the two can be compared line by line:
	//
	//	Session (5hr)    37%   Resets at 14:00
	//	Weekly (7 day)   52%   Resets Thu, Nov 12 at 03:00
	fmt.Fprintf(w, "measured %s\n", st.At.Local().Format("2006-01-02 15:04 MST"))
	last := ""
	for _, x := range st.Windows {
		if x.Account != last {
			last = x.Account
			plan := x.Plan
			if plan == "" {
				plan = "unknown"
			}
			fmt.Fprintf(w, "Account: %s\nPlan: %s\n", x.Account, plan)
		}
		line := fmt.Sprintf("%-16s", x.Label)
		if x.Limit > 0 {
			line += fmt.Sprintf(" %3d%%", panelPercent(x.UsedPercent))
			if x.ResetsAtUnix > 0 {
				line += "   Resets " + panelReset(x.ResetsAtUnix, x.Label, now())
			}
		} else {
			line += "   ?"
		}
		fmt.Fprintf(w, "%s   [%s]\n", line, strings.ToUpper(string(x.Verdict)))
	}
	for _, p := range st.Problems {
		fmt.Fprintf(w, "problem: %s\n", p)
	}
	fmt.Fprintf(w, "verdict: %s\n", strings.ToUpper(string(st.Verdict)))
	return nil
}
