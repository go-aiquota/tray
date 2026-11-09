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

```sh
GOBIN=~/.local/bin go install github.com/go-aiquota/tray/cmd/aiquota@latest
GOBIN=~/.local/bin go install github.com/go-aiquota/tray/cmd/quotaclaim@latest
```

`aiquota status [--account A] [-o json|text]` loads the Claude plugin, asks
LIVE for every window (5 h session, weekly, per-model) and prints used %,
the reset time (local, 24 h) and the plan. Exit status: **0** available,
**1** exhausted, **3** UNKNOWN (could not read — never reported as available
or exhausted), 2 usage error. Each run writes a stamp to
`os.UserConfigDir()/aiquota/last-status.json` (time, windows, verdicts; no
credential).

`-o statusbar` prints one JSON object `{version, text, tooltip, level,
verdict}` for an editor status bar: `text` is `Claude 30% · 5%` (session ·
weekly) or `Claude ?` when UNKNOWN (the reasons are in the tooltip, never a
number), `level` is `ok`, `warn`, `error` or `unknown` (`--warn 75`,
`--critical 90`, percent of the worst window; an exhausted window is always
`error`). The tooltip lists every window with its reset and the measurement
time. A client asks for the contract it understands with `--format-version`: 1 (the
default, exactly the object above) or 2 (v0.5.0; since v0.4.0 as the only
format), which also carries `items`, one per window (session,
weekly, and a per-model window when above zero), each with `text`, `percent`,
`band` (0 to 7, equal steps of 12.5 %; `--bands N`, 2 to 8, for coarser steps;
-1 when unknown), `color` (hex) and `background` (`none`, `warning`, `error`).
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
