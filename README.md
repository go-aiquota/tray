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

**First live read (macOS).** The credential is a login-keychain item
(`go-aiquota:claude`) whose ACL trusts binaries by cdhash. A newly installed
`aiquota` is not on that list, so its first read raises a keychain dialog that
a headless session cannot answer (`aiquota status` then reports UNKNOWN after
`--timeout`). One-time step, per installed version: run `aiquota status` in
Terminal.app and click **Always Allow** (login password). No secret is copied
anywhere.

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
