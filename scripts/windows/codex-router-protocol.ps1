[CmdletBinding()]
param([Parameter(ValueFromRemainingArguments = $true)][string[]] $Ignored)

$ErrorActionPreference = "Stop"
$installRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$launcher = Join-Path $installRoot "Connect Codex Router Account.ps1"
$dashboard = Join-Path $installRoot "open-dashboard.ps1"
if (-not (Test-Path -LiteralPath $launcher -PathType Leaf)) { throw "Router account launcher is missing; rerun install.ps1." }
if (-not (Test-Path -LiteralPath $dashboard -PathType Leaf)) { throw "FLOW dashboard launcher is missing; rerun install.ps1." }
$output = @(& $launcher -Action Connect -NewAccount -NoOpen -NoWait)
$code = $LASTEXITCODE
if ($code -ne 0) {
    & $dashboard -InstallRoot $installRoot | Write-Output
    exit $code
}
$event = $output |
    Where-Object { $_ -is [string] -and $_.TrimStart().StartsWith("{") } |
    ForEach-Object { try { $_ | ConvertFrom-Json } catch {} } |
    Where-Object { $_.event -eq "sign_in_required" } |
    Select-Object -First 1
if (-not $event -or [string]::IsNullOrWhiteSpace([string]$event.accountId) -or [string]::IsNullOrWhiteSpace([string]$event.attemptId) -or [string]::IsNullOrWhiteSpace([string]$event.verificationUrl)) {
    throw "Router did not return a complete sign-in handoff."
}
$dashboardUrl = & $dashboard -InstallRoot $installRoot -ConnectAccount ([string]$event.accountId) -ConnectAttempt ([string]$event.attemptId) -ConnectUrl ([string]$event.verificationUrl) | Select-Object -Last 1
Start-Process ([string]$event.verificationUrl)
Write-Output $dashboardUrl
exit $code
