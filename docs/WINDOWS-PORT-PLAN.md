# Windows port plan

## Objective

Run the subscription router on Windows without modifying the installed
official Codex/ChatGPT package, while preserving the existing macOS patcher.

## Chosen boundary

The Windows installer builds a versioned user-local `codex-mux.exe`. One
per-user daemon owns account state, Codex children, a dynamic localhost
dashboard, and an authenticated stdio bridge. The bridge forwards compatible
app-server stdio to the daemon; it never starts a competing account pool. The
launcher sets `CODEX_MUX_HOME` and `CODEX_HOME` to independent user-local state
directories.

This avoids copying or patching the protected WindowsApps package and keeps the
official installation and its files unchanged.

## In scope

- Windows-safe real-executable discovery, `.exe` handling, and child-process
  termination in the Go mux.
- A PowerShell installer that discovers the installed official app, stages and
  verifies a versioned mux, starts exactly one daemon, registers per-user logon
  startup, verifies readiness, and opens the dashboard in the default browser.
- Explicitly validate and pass a runnable Windows backend through
  `CODEX_MUX_REAL_CODEX`; prefer the user-local backend already used by the
  official app, and retain the protected package's absolute
  `resources\\codex.exe` only as an integrity reference. Fail closed when no
  runnable backend is found unless `-CodexExecutable` is supplied.
- Use an explicit router-owned primary `CODEX_HOME` under the user-local
  install root (`%LOCALAPPDATA%\\Codex Subscription Router\\primary-codex-home`)
  so the install cannot clobber or silently reuse the existing official
  `%USERPROFILE%\\.codex` state.
- Put every Windows backend tree in a kill-on-close Job Object and prove daemon
  exit cannot leave a descendant behind.
- A same-origin dashboard with one-use bootstrap, browser session, CSRF,
  explicit login states, account recovery, usage, and codex-lb export import.
- Documentation, static checks, and focused Windows install/launch proof.
- A clear rerun path that preserves the router state and refuses unsafe source
  or destination collisions.

## Out of scope

- Rewriting the version-sensitive renderer/account-menu injection for the
  current Windows bundle.
- Wiring the current Windows GUI's package-managed local app-server to the
  standalone router; the inspected build does not consume the router override
  for that path.
- Windows Computer Use identity/signing changes.
- Claiming provider authentication, codex-lb migration, or live multi-account
  routing until those boundaries are exercised separately on the installed
  artifact.
- Any modification of the official app package or deletion of existing Codex
  state.

## Required gates

1. Plan review: an independent reviewer confirms the boundary is a meaningful
   Windows-compatible router path and that the official package remains
   untouched.
2. Source proof: Go tests, Go vet, Python/JS checks, and release checks pass.
3. Install proof: the installer resolves the actual local Windows package,
   stages the exact versioned mux, preserves package hashes, starts one daemon,
   verifies its PID/build/address, registers startup, and opens setup.
4. Runtime proof: the installed daemon initializes the selected backend; the
   dashboard renders and account/recovery states work; child cleanup, package
   pre/post hashes, ACLs, and runtime receipts are captured without exposing
   credentials. Existing `%USERPROFILE%\\.codex` remains unchanged.
5. Acceptance review: an independent reviewer inspects the frozen commit and
   install/runtime receipts. Missing provider or UI parity remains
   `UNVERIFIED`.

## Acceptance route

The implementation owner integrates the exact commit. A separate reviewer
returns `APPROVE`, `CORRECT`, or `BLOCKED` for the exact artifact and gates.
The CTRL composes the final user-facing verdict only after that acceptance
receipt exists.
