# Changelog

All notable changes follow [Keep a Changelog](https://keepachangelog.com/) and
this project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.2.0] - 2026-08-17

### Added

- Windows per-user daemon with a single-owner lease, dynamic protected runtime
  receipt, app-server bridge, logon startup, readiness, and controlled restart.
- Minimal same-origin localhost dashboard for subscription setup, usage, reset
  times, routing inclusion, rename, login recovery, and account removal.
- One-time migration of canonical Codex auth exports from codex-lb, with source
  ownership confirmation, private backups, and post-import account proof.
- Versioned Windows installation, binary/backend hash enforcement, exact-build
  readiness, rollback, default-browser launch, and recovery-aware shortcuts.
- Browser session, CSRF, Host/Origin, bootstrap replay, process-tree, ACL,
  account idempotency, child recovery, and login-state tests.

### Changed

- Windows state now uses a protected verified DACL for only the current user
  and SYSTEM.
- Disabled subscriptions do not run credential-bearing child processes, and
  removed account homes are archived rather than deleted.
- Documentation now separates the standalone Windows product boundary from
  the patched macOS desktop experience.

## [0.1.0] - 2026-08-15

### Added

- One-command macOS installer with prerequisite checks, signed rebuilds,
  recoverable upgrades, and automatic launch.
- Reset-aware routing that prioritizes weekly quota at risk of expiring and
  gives a bounded boost to subscriptions with banked usage resets.
- Multi-subscription routing with quota-aware balancing and sticky threads.
- Account isolation, device-code sign-in, pooled usage, and quota failover.
- Native account menu, masked emails, plan labels, and profile photos.
- Combined Profile statistics with per-account selection.
- Account-scoped Apps and MCP connection state in Settings → Plugins.
- Per-account rate-limit reset selection and pooled depletion handling.
- Independently signed Appshots and Computer Use support.
- Fail-closed upstream compatibility checks and deepest-first nested helper signing.
- Loopback-only, token-authenticated diagnostic UI states.
- Source-only CI, draft release automation, security documentation, and smoke tests.

[Unreleased]: https://github.com/flowwweb/codex-subscription-router/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/flowwweb/codex-subscription-router/releases/tag/v0.2.0
[0.1.0]: https://github.com/flowwweb/codex-subscription-router/releases/tag/v0.1.0
