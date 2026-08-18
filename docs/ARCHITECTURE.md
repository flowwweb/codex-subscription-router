# Architecture

The independently built desktop uses bundle identifier `app.cdxmux.multi`; its
Computer Use helper uses `com.cdxmux.sky.CUAService`. Neither identifier is used
by the official ChatGPT installation. These identifiers and the `.codex-mux`
state directory remain stable across the product rename so existing macOS
privacy grants, connected accounts, and sticky thread ownership continue to
work.

Codex Subscription Router replaces the copied app's bundled `codex` executable
with a small Go multiplexer and keeps the original binary beside it as
`codex.real`.

## Request routing

The desktop app opens one JSON-RPC app-server connection to the multiplexer.
The multiplexer starts one real app-server child for every enabled account,
each with its own `CODEX_HOME` and `CODEX_SQLITE_HOME`.

New threads are assigned using a quota-urgency score: weekly percentage
remaining divided by the hours until that account resets. Banked usage resets
add a capped bonus, while short-window usage, existing pinned-thread count, and
stable account order break close results. Reset-credit metadata is fetched in
parallel, cached for five minutes, and treated as neutral when unavailable.
Once a thread ID is known, `state.json` persists its owner. Requests, responses,
approvals, and notifications are rewritten only as needed to preserve one
coherent desktop session.

If the owner is depleted, the multiplexer resumes the rollout on an account
with capacity and updates ownership. Threads do not migrate for ordinary load
balancing.

## Account isolation

The Primary account uses `~/.codex`. Added accounts use
`~/.codex-mux/accounts/<id>/codex-home`. Managed configuration is copied from
the Primary account, excluding credential-store settings and project trust.
Each isolated account forces file-backed CLI and MCP OAuth credentials.

## Desktop integration

The patcher extracts `app.asar`, verifies exact upstream anchors, inserts the
account UI, disables self-update, and repacks the archive with an updated
integrity hash. The app receives a separate Chromium profile and URL scheme.

The copied Computer Use service, Node runtime, and callers are re-signed under
one Apple team. The helper uses a separate bundle identity and socket, avoiding
the official app's privacy grants and app-group container.

On Windows, the installer leaves the official Windows package in place and
installs one per-user daemon plus a thin app-server stdio bridge. The daemon
holds an OS-level single-owner lease, owns every account child, binds the
stable `127.0.0.1:48123` control endpoint plus private dynamic-loopback
readiness/bridge endpoints, and writes their PID, build, instance, and
addresses to a protected atomic runtime receipt.
`CODEX_MUX_REAL_CODEX` points at the user-local backend used by the official
Windows app (or an explicitly supplied backend), while `CODEX_MUX_HOME` and
`CODEX_HOME` point to router-owned state and primary-account directories. The
protected package's bundled
`resources\\codex.exe` is retained for integrity verification but is not
assumed runnable outside the package identity. The current Windows GUI build
does not consume the router override for its local app-server, so this adapter
does not claim GUI/account-menu or Computer Use identity parity.

## Plugin behavior

Plugin definitions and managed MCP configuration are shared. The Plugins page
adds an account selector and marks Apps, MCP status, and MCP OAuth requests with
the selected account ID. The multiplexer removes that private routing marker
before forwarding the strict RPC request to the chosen child.

## Control API

The macOS renderer retains its loopback token contract. Windows uses the
daemon's stable control address. Embedded dashboard assets are served
same-origin; a fresh fragment nonce is exchanged once for an HttpOnly,
SameSite=Strict browser session. Mutations require CSRF, Host and Origin are
restricted to the exact numeric-loopback listener, and SSE uses the session
without a URL credential. The service exposes account metadata, usage, thread
ownership, explicit login states, import/logout/remove actions, and events; it
never returns OAuth tokens.

The Windows installer stages a versioned binary, verifies the selected backend
and mux hashes, switches an atomic current configuration, waits for exact-build
daemon readiness, and rolls configuration back when readiness fails. A
least-privilege per-user logon task starts the daemon after sign-in.
