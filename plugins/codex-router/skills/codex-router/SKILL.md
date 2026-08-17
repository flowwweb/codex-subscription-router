---
name: codex-router
description: Show Codex Router status or connect OpenAI accounts through its fixed installed Windows launcher. Use for an explicit `$codex-router` invocation, "show router status," "connect an OpenAI account," or "connect another OpenAI account." Do not use for generic or unrelated account connections such as Gmail, GitHub, cloud providers, or other services.
---

# Codex Router

Use the installed launcher only. Status is the default action. Keep a connection attached until the launcher emits a terminal event.

## Trust boundary

- Resolve the launcher as `%LOCALAPPDATA%\Codex Subscription Router\Connect Codex Router Account.ps1`.
- Invoke only that exact file with `-Action Status` or `-Action Connect`.
- Add `-NewAccount` only for "connect another OpenAI account."
- Add `-AccountId` only with an exact account ID returned by the current status result. Never pass a user-provided label, ID, path, URL, or other arbitrary string.
- Never read router tokens or auth files, call loopback endpoints, open the localhost dashboard, patch the native Codex app, or reconstruct private router requests.
- Never print tokens, raw provider payloads, `device_code`, cookies, or auth-file contents.
- Treat only HTTPS verification URLs on `chatgpt.com`, its subdomains, `auth.openai.com`, or its subdomains as trusted. The launcher performs the browser open; never open a different URL yourself.

## Invoke the launcher

Use PowerShell's call operator with an argument array. Do not build a command string, use `Invoke-Expression`, or launch through a shell-expanded user value.

```powershell
$routerLauncher = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Codex Subscription Router\Connect Codex Router Account.ps1'
& $routerLauncher -Action Status
```

For connection, first run status and resolve the intent below. Then invoke one fixed form:

```powershell
& $routerLauncher -Action Connect
& $routerLauncher -Action Connect -NewAccount
& $routerLauncher -Action Connect -AccountId $accountIdFromStatus
```

Keep the connection command in the foreground and relay the challenge before waiting for its terminal event.

## Resolve intent

### Status

For `$codex-router` with no action or "show router status," run `-Action Status` only. Never open a browser or start sign-in. Report overall readiness and compact per-account state from the returned status.

### Connect an OpenAI account

Run status first.

- With one account needing sign-in or repair, use its returned ID with `-AccountId`.
- With several candidates, ask `Which account should I connect: Personal or Work?` Resolve the chosen returned label to its returned ID; never pass the label.
- With no reusable candidate, run `-Action Connect` without an account ID and let the router atomically select or create the slot.
- For an explicitly named account, use its ID only when the current status returned an unambiguous matching label. Otherwise ask the user to choose from returned labels.

### Connect another OpenAI account

Run status, then invoke `-Action Connect -NewAccount`. Do not reuse an existing slot for this intent.

## Present events

Use only sanitized public fields emitted by the launcher. Keep account IDs internal. Do not claim the browser identity was selected or switched; OpenAI's page determines which identity signs in.

- Challenge: `OpenAI is open. Enter ABCD-EFGH to connect Account 2. I'll wait here.`
- Connected: `Account 2 is connected. Codex Router has 2 accounts ready.`
- Expired: `That code expired. Say "connect account" to get a new one.`
- Browser-open failure: `I couldn't open OpenAI. Open the trusted link and enter ABCD-EFGH. I'll wait here.`
- Ambiguous selection: `Which account should I connect: Personal or Work?`
- Install, integrity, or start failure: `Codex Router needs repair: <classified reason>. Re-run the installer, then try again.`
- Unexpected URL: `OpenAI returned an unexpected sign-in address, so I didn't open it. Try again.`
- Provider failure: `OpenAI didn't connect the account: <sanitized provider failure>. Try "connect account" again.`
- Cancelled: `Connection cancelled. Nothing changed.`
- Cancellation unconfirmed: `I couldn't confirm cancellation. I'll check router status before trying again.`

If the launcher is missing, fails integrity checks, or emits malformed output, stop and use the repair copy. Do not fall back to direct HTTP, the dashboard, or a different executable.
