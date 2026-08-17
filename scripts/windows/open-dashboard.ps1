[CmdletBinding()]
param([switch] $NoBrowser)

$ErrorActionPreference = "Stop"
$installRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$configPath = Join-Path $installRoot "router-config.json"
if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) {
    throw "Router configuration is missing; rerun install.ps1."
}
$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
& (Join-Path $installRoot "start-router.ps1") | Out-Null
$env:CODEX_MUX_HOME = [string]$config.stateRoot
$url = (& ([string]$config.muxExecutable) dashboard-url | Select-Object -Last 1).Trim()
if ($LASTEXITCODE -ne 0 -or $url -notmatch '^http://127\.0\.0\.1:\d+/(?:#bootstrap=[A-Za-z0-9%]+)?$') {
    throw "Router did not return a safe local dashboard URL."
}
Write-Output $url
if (-not $NoBrowser) {
    Start-Process $url
}
