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
[extension's install guide](https://github.com/go-aiquota/vscode-statusbar#install).

**Install with Go** (the provider plugin is a separate module):

```sh
GOBIN=~/.local/bin go install github.com/go-aiquota/tray/cmd/aiquota@latest
GOBIN=~/.local/bin go install github.com/go-aiquota/tray/cmd/quotaclaim@latest
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
the endpoint lists without a value is `–` (verdict `n/a`, never a number, and
outside the overall verdict). `--overlay` reproduces the panel's rows exactly,
for the equality check; `-o json --raw` prints the usage response as received
(it holds no credential) so you can see everything Claude returns.

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
