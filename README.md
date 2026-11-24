<p align="center"><img src="https://raw.githubusercontent.com/go-aiquota/brand/main/social/go-aiquota.png" alt="go-aiquota/tray" width="720"></p>

# go-aiquota / tray

[![CI](https://github.com/go-aiquota/tray/actions/workflows/ci.yml/badge.svg)](https://github.com/go-aiquota/tray/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-aiquota/tray.svg)](https://pkg.go.dev/github.com/go-aiquota/tray)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)

A cross-platform menu-bar app that monitors AI usage quotas (session/weekly
limits) across multiple accounts in parallel — Claude Max, Claude Team
Premium, Claude Team Standard, and (via the plugin architecture in
[`go-aiquota/proto`](https://github.com/go-aiquota/proto)) other providers
as plugins are added.

## Status

Early. Building bottom-up:

- [x] **`account`** — non-secret account metadata (`accounts.json`) +
  credential storage via [`go-keyring/keyring`](https://github.com/go-keyring/keyring),
  gated behind the platform's presence check (Touch ID / equivalent) where
  the host can honor it.
- [ ] Plugin manager — launching and polling provider plugins
  (`go-aiquota/plugin-claude` first) over the `go-aiquota/proto` gRPC
  contract.
- [ ] Tray icon + menu (`go-widgets/tray`).
- [ ] Onboarding window: an isolated, per-account login flow rendered
  through [`go-webengine/engine`](https://github.com/go-webengine/engine)'s
  `LiveDocument` (real focus/type/click against the actual provider login
  page, no manual cookie copying) — see that project's PRs #89–95 for the
  interactive-rendering foundation this depends on.

## Command line: `aiquota` and the `quotaclaim` gate

**Install without Go.** Each [release](https://github.com/go-aiquota/tray/releases)
carries one archive per platform, `aiquota_<tag>_<os>_<arch>.tar.gz` (`.zip` on
Windows), and a `SHA256SUMS` file. An archive holds `aiquota`, `quotaclaim` and
`go-aiquota-plugin-claude` (the Claude provider, which `aiquota` starts and
looks up under exactly that name, on `PATH` first and then in its own
directory since v0.8.1) and the licence: put the programs in one directory,
ideally one that is on your `PATH`. Check the download against
`SHA256SUMS` first (`shasum -a 256 -c --ignore-missing SHA256SUMS` on macOS and
Linux, `Get-FileHash` on Windows). Operating systems: Linux, macOS, Windows,
FreeBSD, OpenBSD and NetBSD, each for amd64 and arm64. CI builds all twelve on
every pull request, but only Linux, macOS and Windows are *tested* on a runner;
the three BSDs are built and never run by CI. The binaries are static
(`CGO_ENABLED=0`), built by `go run ./internal/reldist`, and carry a GitHub
provenance attestation (`gh attestation verify <archive> --repo go-aiquota/tray`).
macOS may quarantine a downloaded binary: see the
[install guide](https://go-aiquota.github.io/docs/main/install/) (also in the
[extension's README](https://github.com/go-aiquota/vscode-statusbar#install)).

**Install with Go** (the provider plugin is a separate module):

```sh
GOBIN=~/.local/bin go install github.com/go-aiquota/tray/cmd/aiquota@latest
GOBIN=~/.local/bin go install github.com/go-aiquota/tray/cmd/quotaclaim@latest
GOBIN=~/.local/bin go install github.com/go-aiquota/tray/cmd/quotapace@latest
GOBIN=~/.local/bin go install github.com/go-aiquota/plugin-claude@latest
mv ~/.local/bin/plugin-claude ~/.local/bin/go-aiquota-plugin-claude
```

`go install` names the plugin after its module, so it is renamed to the name
`aiquota` looks for (the prebuilt archives already use it).

`aiquota status [--account A] [-o json|text]` loads the Claude plugin, asks
LIVE for every window (5 h session, weekly, per-model) and prints used %,
the reset time (local, 24 h) and the plan. Exit status: **0** available,
**1** exhausted, **3** UNKNOWN (could not read — never reported as available
or exhausted), 2 usage error. Each run writes a stamp to
`os.UserConfigDir()/aiquota/last-status.json` (time, windows, verdicts; no
credential).

By default `aiquota status` shows **every window the usage endpoint returns**,
not only the rows of Claude Code's panel: known windows keep the panel's labels
and order (Opus included), unknown ones get a label derived from their key, and
`limits[]` entries are added without duplicating the top-level windows. A window
the endpoint lists without a value is `–   no separate limit on this plan`
(verdict `n/a`, never a number, and outside the overall verdict): the plan has no
window of its own for that model, which is governed by the session and weekly
windows. It does NOT mean blocked or unmeasured. `--overlay` reproduces the panel's rows exactly,
for the equality check; `-o json --raw` prints the usage response as received
(it holds no credential) so you can see everything Claude returns.

**Can I use model X right now?** `aiquota status --model sonnet` (case-insensitive)
answers for that one model on the active login: the session and weekly windows
plus the window scoped to the model when the plan has one, each with its %, reset
(local, 24 h), then the account, plan and the reading's time. Exit **0**
available, **1** exhausted (names the limit and its reset), **3** UNKNOWN (could
not read). A plan with no window of its own for the model prints
`no separate sonnet limit on this plan` and is available when the session and
weekly windows are. `-o json` adds a `model` object (verdict, `scoped`, `limits`,
`blocking`, `reason`) to the stamp. Never relay an old 429 as "model X is
blocked": run this.

**One reading for every process.** The usage endpoint rate limits, and a machine
runs several editor windows, the CLI and the `quotaclaim` gate. They share one
reading, kept next to the stamp (`cache-*.json`, no credential): within
`--min-interval` (default 1 m) a process reuses the last good reading and shows
its real measurement time; one process at a time fetches (a lock file); a
`Retry-After` is written down and honoured by all of them; and while the
endpoint cannot be asked the last good reading is served marked `stale` (its age
shown) for up to `--max-stale` (default 10 m), after which the answer is UNKNOWN.
`--fresh` fetches now, still respecting a `Retry-After`; `--min-interval 0`
disables sharing. `quotaclaim` keeps its own max-age check against the real
measurement time, so a cached or stale reading counts with its true age.

`-o statusbar` prints one JSON object `{version, text, tooltip, level,
verdict}` for an editor status bar: `text` is `Claude 30% · 5%` (session ·
weekly) or `Claude ?` when UNKNOWN (the reasons are in the tooltip, never a
number), `level` is `ok`, `warn`, `error` or `unknown` (`--warn 75`,
`--critical 90`, percent of the worst window; an exhausted window is always
`error`). The tooltip lists every window with its reset and the measurement
time. A client asks for the contract it understands with `--format-version`: 1 (the
default, exactly the object above) or 2 (v0.5.0; since v0.4.0 as the only
format), which also carries `items`, one per window (session,
weekly, and every other window the endpoint returns, a 0 % one included), each with `text`, `percent`,
`band` (0 to 7, equal steps of 12.5 %; `--bands N`, 2 to 8, for coarser steps;
-1 when unknown), `color` (hex) and `background` (`none`, `warning`, `error`).
A window at 100 % (or exhausted) carries its reset timestamp in its text,
`7d 100% → 2026-11-13 03:00` (v0.9.0; local, 24 h, always with the date).
The 8 steps are light green, dark green, light yellow, dark yellow, light
orange, dark orange, then the theme's warning and error backgrounds: VS Code's
extension API honours only those two backgrounds, so every other step is a
text colour, measured at WCAG contrast of at least 3:1 against the default
status bar (#007ACC in Dark+ and Light+, #68217A with no folder) by a Go test.
UNKNOWN is one grey "Claude ?" item. `--render 1` (v0.6.0) prints the generic
render description the extension draws as it is: `{render, items, detail,
poll_seconds, level, verdict}` with, per item, `id`, `text`, `tooltip`, `color`
(hex or theme colour id), `background`, `priority` (an offset from the user's
base), `alignment` and `command` (`detail`, `refresh`, `none`). The number of
items, their order, labels, colours, bands, click actions and the poll interval
are decided in Go, so changing the look is a `go install` of a new `aiquota`
and a click on the item, never a reload of the editor; render 1 stays
supported when a later render exists. The [VS Code
extension](https://github.com/go-aiquota/vscode-statusbar) shows them
permanently and holds no logic of its own.

`quotaclaim` is a Claude Code `Stop` / `SubagentStop` hook that refuses a
reply asserting a usage-limit state ("weekly limit", "hit your limit",
"resets Nov 12", "limite hebdomadaire", "bloqué jusqu'au", ...) unless a stamp
younger than `--max-age` (default 10 min) backs it. It blocks with
`{"decision":"block","reason":...}` naming `aiquota status`
([hooks reference](https://code.claude.com/docs/en/hooks)). Add to
`~/.claude/settings.json`:

```json
{"hooks": {
  "Stop":         [{"hooks": [{"type": "command", "command": "~/.local/bin/quotaclaim"}]}],
  "SubagentStop": [{"hooks": [{"type": "command", "command": "~/.local/bin/quotaclaim"}]}]
}}
```

The matcher claims only statements about Claude's OWN usage ("5-hour limit",
"weekly limit", "limite hebdomadaire", "hit your ... limit", "usage limit",
"resets <date>" beside one of those, "bloqué jusqu'au", "quota Claude",
"Opus/Sonnet limit"). Gateway vocabulary (soft/hard quota, `rate_limit_exceeded`,
Retry-After, budgets, `max_tokens`, GitHub API rate limits) and denials ("not
your usage limit") are never claims; the table test holds 39 such negatives.
`quotaclaim scan` replays the matcher over `~/.claude/projects/*/*.jsonl`
(read-only). With no usable measurement (none, stale, UNKNOWN) a claim passes
only if its own sentence says "unknown" / "not measured"; a second Stop in the
same turn (`stop_hook_active`) is never blocked, to avoid a loop.

### `quotapace`: the quota pacing gate

`quotapace` is a Claude Code `PreToolUse` hook on the `Agent` tool that paces
the quota of **the plan in use**, so it is spent at the rate that empties it
exactly at its reset instead of hitting the wall days early.

- **Only the active account decides**: the login Claude Code bills to, which
  `aiquota status` measures by default (its stamp lists that account with its
  plan). It is re-read at every call, so a switch of account during the day is
  followed at the next stamp; the history is keyed by account AND plan, so one
  plan's readings never feed another's. The other accounts and the aggregate
  stay in the `pace` report but never decide a spawn. With several stored
  accounts and no Claude Code login the active one cannot be told: the hook
  allows.
- **Exactly the windows that plan reports**: `5h` and `7d`, plus a
  model-specific weekly window (`Weekly Opus`, `Weekly Sonnet`, ...) when the
  plan has one; that window judges only a spawn on its model. A window listed
  without a value (n/a) is no constraint, and an unknown plan allows.
- **The recent rate, not the cumulative one**: a window is over pace when the
  slope of the readings quotapace recorded in `aiquota/pace-history.json` over
  the last hour (`--recent`, at least `--min-span` apart) exceeds the sustainable
  rate (`remaining% / hours to reset`, from the window's own reset) by more than
  `--tolerance` (default 0.10), once `--min-elapsed` (5%) of the window has
  passed. A burst that has stopped stops refusing. Fewer than two readings in the
  lookback is `no_recent`, which allows. The percentage is an integer, so one
  tick inside the hour reads as 1 %/h, above a 7d sustainable rate of 0.3 to
  0.4 %/h: widen `--recent` or `--min-span` if that is too nervous. The history
  only records when quotapace runs (a spawn, or `pace`).
- **Advisory first, refusal only for opus**: over pace, a non-opus spawn is
  allowed with an `additionalContext` note asking to slow down and prefer sonnet
  or haiku sub-agents, with a concurrency derived from the plan's windows (your
  usual `--max-agents`, default 3, scaled by sustainable / recent). An opus
  spawn (a model containing `opus`, or the `reviewer` agent with no model) is
  **refused** (`permissionDecision: deny`, with the numbers and the projected
  exhaustion time) unless its prompt holds a line `pace-waiver: <reason>`
  (logged to `aiquota/pace-waivers.log`; it then passes with the note).
- **Fresh without a manual `aiquota status`**: a missing stamp or one older than
  `--max-age` (default 15 min) is refreshed by the hook itself, running
  `aiquota status --stamp <file>` (`--refresh-cmd`; default `aiquota` beside the
  executable, else on PATH) under a single-flight lock, waiting at most
  `--refresh-timeout` (3 s, the refresher is left to finish) and at most once per
  `--refresh-every` (5 min, failures included; 0 disables it). No stamp after
  that, an unreadable one or a stale one allows: a refusal is not a verdict when
  the gate cannot read.

Accounts are always labels, never an email or id. A plan's capacity in tokens is
not derivable from a stamp (percentages only), so no such figure is shown.

```json
{"hooks": {"PreToolUse": [{"matcher": "Agent",
  "hooks": [{"type": "command", "command": "~/.local/bin/quotapace"}]}]}}
```

Report, per account and aggregate (the mean of the accounts): `quotapace pace`,
or `quotapace pace --json` for the stable document (`schema: 2`; fields are only
added within a version): `at`, `stamp_at`, `stamp_age_seconds`, `stamp_fresh`,
`gate {state, until}`, `accounts[] {account, plan, active, gate, windows[]}` and
`aggregate[]`, each window being `{window, model, slowdown_factor, used_percent,
elapsed_percent, recent_per_hour (or null), rate_per_hour (cumulative),
sustainable_per_hour, projected_at_reset_percent, projected_exhaustion (RFC 3339
or null, at the recent rate), resets_at, state (ok | over_pace | exhausted |
too_early | no_recent)}`.

**Switch it off at will**, with no settings.json edit and no uninstall:

```
quotapace pace off [--for 2h] [--account <label>]   # --for re-enables itself on expiry
quotapace pace on  [--account <label>]
quotapace pace status
```

The switches live in `os.UserConfigDir()/aiquota/pace-gate.json`, global and per
account. While off the hook allows everything and prints nothing, but `pace`
keeps printing the numbers and shows `gate: off until ...`. An account that is
off counts as having headroom. The deny message names these commands.

`aiquota status` keeps the plugin client's own log (handshake, mTLS, stdio)
quiet at Warn; `--debug` shows it on stderr for diagnosis.

**Same view as Claude Code.** By default `aiquota status` queries **Claude
Code's own login** (plugin-claude's "Claude Code" source, `GET
api.anthropic.com/api/oauth/usage` with the token Claude Code keeps in the
Keychain item `Claude Code-credentials`): the same account, plan, windows and
percentages as the VS Code extension's *Account & usage* panel, printed with
the panel's labels, order and wording so the two compare line by line:

```
Account: Claude Code
Plan: max
Session (5hr)     37%   Resets at 14:00   [AVAILABLE]
Weekly (7 day)    52%   Resets Thu, Nov 12 at 03:00   [AVAILABLE]
Weekly Sonnet      8%   Resets Thu, Nov 12 at 03:00   [AVAILABLE]
```

Rows the panel hides (null windows) are not printed. Accounts onboarded
through the claude.ai cookie flow are the fallback, used only when no Claude
Code login exists or when named with `--account`. The token is never
refreshed (Claude Code renews its own login): once expired the status is
UNKNOWN, `token expired, open Claude Code`. The read goes through
`/usr/bin/security`, which the item's ACL already trusts, so there is no
keychain dialog and nothing to re-approve after a reinstall. A stored
claude.ai account's keychain item is different: its ACL pins each binary by
cdhash and a first read prompts once ("Always Allow").

Why it exists: a session once told a user "blocked until Nov 12" from a dead
subagent's error message, after the user had changed plan. A limit is a
measurement, not a memory.

## Security

A credential is never written to `accounts.json` (metadata only), never
logged, and lives only in the OS keyring. See `account/store.go`'s doc
comments for the presence-gating design (and its current constraint: the
Touch ID gate specifically needs a code-signed macOS build — an unsigned
dev build falls back to ungated-but-still-OS-keyring storage rather than
losing the credential, and records which happened per account).
