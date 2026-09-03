# go-aiquota / tray

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

## Security

A credential is never written to `accounts.json` (metadata only), never
logged, and lives only in the OS keyring. See `account/store.go`'s doc
comments for the presence-gating design (and its current constraint: the
Touch ID gate specifically needs a code-signed macOS build — an unsigned
dev build falls back to ungated-but-still-OS-keyring storage rather than
losing the credential, and records which happened per account).
