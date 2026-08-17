# Codex Router plugin plan

## Outcome

Let a Windows user ask Codex to show router status or connect an OpenAI account
without opening the localhost dashboard or modifying the signed Codex app.

## Supported surface

- Add stable installed commands:
  - `codex-mux status --json`
  - `codex-mux connect-account --wait --open --json`
- Install a fixed `Connect Codex Router Account.ps1` launcher that verifies the
  installed mux through the existing start and integrity path before invoking
  those commands.
- Package one repo-owned `codex-router` Codex plugin and skill. The skill may
  invoke only that installed launcher; it must not reconstruct control URLs,
  read tokens, call the dashboard, or patch the official app.

## Authority and trust

- The client reads the protected control token in-process from the configured
  state root. It never accepts the token through arguments or prints, logs, or
  exports it.
- The client validates the signed runtime receipt with the existing exact
  readiness probe. Dedicated task-action requests also carry the receipt's
  instance identity, which the daemon verifies before mutation.
- All control traffic is authenticated loopback traffic to the receipt's exact
  control address. Stale receipts, daemon restarts, PID reuse, changed builds,
  and mismatched instances fail closed.
- One daemon-owned operation atomically selects or creates an account and starts
  a login attempt. It reuses an explicitly selected account or the sole account
  needing sign-in/repair, reports ambiguity without mutation, and creates a new
  slot only for an explicit connection intent with no reusable candidate or an
  explicit `--new` request.
- Login is allowed for a paused account without enabling it for routing.
- Only the server's allowlisted HTTPS OpenAI/ChatGPT sign-in URL, optional
  sanitized user code, public attempt state, account identity, and compact
  routing status may cross the task-action boundary. Raw provider results,
  authorization codes, auth files, cookies, and control tokens never cross it.
- The client revalidates the sign-in URL before opening it. Browser launch uses
  an argument-array process call, never a shell command. Launch failure is
  reported while leaving the trusted link available.

## Interaction contract

| User intent | Behavior |
| --- | --- |
| `$codex-router` or show router status | Run status only. Never open a browser or start sign-in. |
| Connect an account | Reuse the sole repair/sign-in candidate; when several qualify, present their labels and keep internal IDs hidden until the selected label is resolved internally; create a slot only when none can be reused. |
| Connect another account | Create one new slot idempotently, then start sign-in. |
| Explicit account | Connect only that returned account ID. |

The command emits one bounded UTF-8 JSON event per line. It emits the sign-in URL
before waiting, polls with bounded backoff until the server-enforced expiry, and
emits one terminal `connected`, `failed`, `expired`, or `cancelled` event.
Ctrl+C performs a bounded cancellation attempt. JSON encoding escapes control
characters; URLs, labels, errors, and response bodies have explicit size
limits.

User copy stays short:

- `OpenAI is open. Finish signing in to connect Account 2. I'll wait here.`
- `Account 2 is connected. Codex Router has 2 accounts ready.`
- `That sign-in expired. Say "connect account" to try again.`
- `I couldn't open OpenAI. Open the trusted sign-in link. I'll wait here.`
- `Which account should I connect: Personal or Work?`
- `Codex Router needs repair: <classified install, integrity, or start failure>. Re-run the installer, then try again.`
- `OpenAI returned an unexpected sign-in address, so I didn't open it. Try again.`
- `OpenAI didn't connect the account: <sanitized provider failure>. Try "connect account" again.`
- `Connection cancelled. Nothing changed.`

The plugin card is named **Codex Router**, uses the canonical three-wave icon,
and contains no Flowwweb wordmark. Flowwweb remains publisher metadata only.

## Proof plan

1. Unit tests: exact-instance authorization, stale receipt, secret omission,
   malicious URL, control characters, response limits, account selection,
   paused-account repair, ambiguity, new-account idempotency, polling,
   cancellation, browser failure, and terminal states.
2. Repository checks: full Go/vet, JavaScript/Python/shell, Windows installer,
   release metadata, plugin validation, skill validation, marketplace schema,
   diff hygiene, and clean source state.
3. Installed checks: exact build/hash/runtime receipt, fixed launcher path,
   plugin source/cache parity, fresh-task status invocation, and fresh-task
   connection challenge before waiting.
4. Browser/provider proof: trusted OpenAI device page and user-completed account
   connection. Until the user completes authorization, provider completion and
   live multi-account routing remain `UNVERIFIED`.
5. Independent acceptance: architecture/security, skill-trigger/negative-trigger
   behavior, UX/copy, exact source SHA, installed package, and representative
   screenshots. No review may claim a native profile-menu row.
