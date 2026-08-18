[CmdletBinding()]
param([Parameter(ValueFromRemainingArguments = $true)][string[]] $Ignored)

$ErrorActionPreference = "Stop"
$installRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$launcher = Join-Path $installRoot "Connect Codex Router Account.ps1"
$dashboard = Join-Path $installRoot "open-dashboard.ps1"
if (-not (Test-Path -LiteralPath $launcher -PathType Leaf)) { throw "Router account launcher is missing; rerun install.ps1." }
if (-not (Test-Path -LiteralPath $dashboard -PathType Leaf)) { throw "FLOW dashboard launcher is missing; rerun install.ps1." }
$output = @(& $launcher -Action Connect -NewAccount)
$code = $LASTEXITCODE
$output | Write-Output
if ($code -eq 0) { & $dashboard -InstallRoot $installRoot | Write-Output }
exit $code
