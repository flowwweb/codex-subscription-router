# Windows v2 audit and acceptance ledger

Date: 2026-08-17

Scope: the Flowwweb Windows adapter on commit
`3e8d22260dc78c8c1b620722ead7b268b74176ee`, including installer,
process lifecycle, state and credential boundaries, localhost control API,
account onboarding, recovery, documentation, and tests.

The upstream macOS patcher is not a v2 rewrite target. The official Codex
Windows GUI is also not claimed as integrated: the inspected GUI does not
consume this standalone app-server override.

## Executive verdict

The v1 branch builds and its static checks pass, but it is not ready as a
Windows product. Installation starts an uninitialized hidden app-server that
owns the fixed control port, has no durable lifecycle owner, and provides no
browser interface. Multiple processes can race the same state. The fixed-port
fail-open behavior can disclose the durable control bearer to a local process
that binds first.

V2 must replace that lifecycle with one per-user daemon, a protected dynamic
loopback endpoint, a thin stdio bridge, and a minimal same-origin dashboard.
Installation is successful only after the exact installed daemon answers an
authenticated readiness check and the default browser opens the clean setup
page.

## Evidence captured

- `npm run check` passed.
- `npm run release:check` passed.
- `npm run check:windows` passed.
- `git diff --check origin/main...HEAD` passed.
- The installed hidden mux was observed on `127.0.0.1:48123` with an account
  error of `account/read: Not initialized`.
- No service was listening on the normal codex-lb port `2455`; Docker Desktop's
  Linux engine was not running; normal host data directories were absent.
- The current codex-lb source exposes authenticated account export as canonical
  Codex `auth.json`, while storing source tokens encrypted in SQLite or
  PostgreSQL.
- No browser, provider-auth, reboot, crash-recovery, update rollback, or
  official-GUI routing proof exists for v1.

## Finding ledger

| ID | Priority | Finding | Required v2 disposition |
| --- | --- | --- | --- |
| F01 | P0 | Fixed port `48123` is fail-open. A local process can bind first and receive bearer-bearing client requests. | Bind `127.0.0.1:0`, publish the chosen endpoint only in a protected runtime receipt, reject endpoint-ownership failure, remove durable query-token authentication, and prove port squatting cannot receive a credential. |
| F02 | P1 | The installed process has no durable lifecycle owner and exits with stdin, crash, logout, or reboot. | Add one per-user daemon, a non-admin logon task, authenticated readiness, bounded stop/restart, and terminal-close/logon proof. |
| F03 | P1 | State locking is process-local; duplicate muxes can race state, tokens, and account backends. | Acquire an OS-level single-instance lease before state or child access. Make a second owner fail deterministically. The app-server command becomes a thin bridge to the daemon. |
| F04 | P1 | The control API is not a safe browser boundary: Host/Origin policy is incomplete and query-token/SSE usage leaks durable credentials. | Serve embedded assets same-origin; enforce Host, Origin, methods, and content types; exchange a one-use short-lived bootstrap nonce for an HttpOnly SameSite session; require CSRF on mutations; use the session or a short-lived event ticket for SSE. |
| F05 | P1 | The official Windows GUI does not route through this adapter. | Keep the product claim to a standalone app-server router and account dashboard. Show the limitation in setup and docs until an independently proven GUI integration exists. |
| F06 | P1 | Installer auto-launch creates an uninitialized orphan and reports a PID without readiness. | Remove the generic hidden app-server launch. Start the daemon, initialize its account children deliberately, wait for exact-instance readiness, and open the dashboard with a printed fallback URL. |
| F07 | P1 | Crashed children remain stale; disable does not stop them and enable does not repair them. | Remove exited children atomically, restart with bounded backoff and replayed initialization, stop/remove on disable, and ensure a live child on enable. |
| F08 | P1 | Reinstall overwrites files but can leave old code running; updates are not atomic or rollback-safe. | Stage versioned artifacts, verify backend compatibility and hashes, switch an atomic current manifest, restart exactly one owner, verify build identity, and roll back on failed readiness. |
| F09 | P2 | Account creation and login are neither transactional nor idempotent and lack terminal status. | Add idempotency, explicit pending/succeeded/failed/expired/cancelled states, retry without duplication, cancel/remove, and success only when account data confirms connection. |
| F10 | P2 | Windows shutdown kills only the direct child and does not prove descendant cleanup. | Put each backend tree in a kill-on-close Job Object, wait with a bound, and surface cleanup failure. |
| F11 | P2 | Existing explicit ACL entries can survive current hardening. | Replace with a protected DACL containing only the current user and required system principal, then verify effective ACLs after create, atomic rewrite, and reinstall. |
| F12 | P2 | Backend drift and compatibility are recorded but not enforced. | Verify the configured hash/version at launch, run a bounded app-server compatibility probe during install/update, and block or roll back incompatible changes. |
| F13 | P2 | Account creation is persisted before child startup; disabled accounts start at boot. | Commit connected account state only after successful initialization, or retain a disabled recoverable record; do not start disabled accounts. |
| F14 | P3 | Windows guidance is buried in macOS instructions and receipts overstate readiness/version meaning. | Lead with platform selection, separate data-path tables, use accurate package/product versions, and reserve `ready` for a connected account plus a compatible app-server client. |
| F15 | P1 | Core negative-path and browser tests are absent. | Add singleton, port-squat, state-race, control security, fake-backend lifecycle/login, install-twice/update, Job Object, ACL, browser accessibility/responsive, and failure-state coverage. |
| F16 | P1 | Copying codex-lb credentials into a second active refresh owner can cause OAuth refresh-token lineage conflicts. | Support an explicit one-time migration using codex-lb's authenticated canonical `auth.json` export. Back up destination state, import without logging secrets, verify each account, and warn/require that codex-lb routing for those credentials is paused. Do not read its database or encryption key directly. |

## Minimal v2 experience

One responsive page is enough:

1. Status: daemon health and a truthful routing-availability sentence.
2. Subscriptions: account identity, plan, textual short/long usage, reset time,
   include/exclude control, rename, and one connect action.
3. Setup: connect Primary, migrate from codex-lb, connect another, or finish.
4. Recovery: only when needed, with the failure reason and restart action.
5. Technical details: collapsed version and local paths for support.

Do not add navigation, routing-algorithm controls, thread lists, plugins, raw
JSON, logs, telemetry, themes, or profile decoration.

The page must work at 320 CSS pixels and 200% zoom, have 44px targets, visible
keyboard focus, text equivalents for usage, polite live login updates, reduced
motion support, and high-contrast/forced-colors support. Browser sign-in must
remain usable when the page needs to be reopened.

## codex-lb migration contract

The compatible handoff is the current authenticated
`POST /api/accounts/{account_id}/export/auth` response's `codex_auth_json`.
It already contains the canonical Codex token structure. V2 may consume it
through a deliberate local migration flow after the user authenticates to the
running codex-lb dashboard/API.

Migration is not synchronization. Codex-lb and the router must not independently
refresh copied credentials. V2 therefore:

- never opens the codex-lb database or encryption key;
- never writes or prints exported tokens outside the protected account home;
- inventories accounts before importing and asks for one migration decision;
- backs up destination account state and imports idempotently;
- verifies account identity and a bounded authenticated account read;
- reports which accounts migrated without returning token material;
- keeps the backup until the user confirms the new router works;
- documents how to resume codex-lb if the migration is rolled back.

If codex-lb is unavailable, setup falls back to normal ChatGPT browser OAuth
login. The audit did not find a running local codex-lb instance, so real
migration remains unverified until that service and its authenticated session
are available.

## V2 acceptance gates

V2 is acceptable only when all applicable findings above are closed with code,
tests, or an explicit truthful product constraint, and independent reviewers
agree on the exact artifact. Minimum proof:

- clean focused Go, JavaScript, PowerShell, release, and diff checks;
- hostile Host/Origin/CSRF, bootstrap replay, SSE, and port-squat tests;
- two-process singleton and concurrent-start tests;
- fake-backend initialize, login terminal states, crash/restart, disable/enable,
  and failed-add tests;
- Windows install-twice/update/rollback, readiness, scheduled-start, and child
  tree cleanup tests;
- effective ACL checks after install and rewrite;
- browser first-run, connected, pending, failure, offline, keyboard, 320px,
  200% zoom, and forced-colors evidence;
- real installed-runtime receipt with exact PID, build, endpoint, and healthy
  account state;
- codex-lb migration proof only if its local authenticated service is available;
- no claim of official Windows GUI routing without separate runtime evidence.
