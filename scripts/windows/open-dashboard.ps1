[CmdletBinding()]
param(
    [switch] $NoBrowser,
    [string] $InstallRoot,
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$')]
    [string] $ConnectAccount,
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$')]
    [string] $ConnectAttempt,
    [string] $ConnectUrl
)

$ErrorActionPreference = "Stop"
$installRoot = if ([string]::IsNullOrWhiteSpace($InstallRoot)) { Split-Path -Parent $MyInvocation.MyCommand.Path } else { $InstallRoot }
$configPath = Join-Path $installRoot "router-config.json"
if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) {
    throw "Router configuration is missing; rerun install.ps1."
}
$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
$startScript = if (-not [string]::IsNullOrWhiteSpace([string]$config.startScript)) { [string]$config.startScript } else { Join-Path $installRoot "start-router.ps1" }
& $startScript -InstallRoot $installRoot | Out-Null
$env:CODEX_MUX_HOME = [string]$config.stateRoot
$url = (& ([string]$config.muxExecutable) dashboard-url | Select-Object -Last 1).Trim()
if ($LASTEXITCODE -ne 0 -or $url -notmatch '^http://127\.0\.0\.1:\d+/(?:#bootstrap=[A-Za-z0-9%]+)?$') {
    throw "Router did not return a safe local dashboard URL."
}
if ($ConnectAccount -or $ConnectAttempt -or $ConnectUrl) {
    if (-not $ConnectAccount -or -not $ConnectAttempt -or $ConnectUrl -notmatch '^https://auth\.openai\.com/oauth/authorize\?') {
        throw "Router returned an incomplete or unsafe sign-in handoff."
    }
    $fragment = "connectAccount={0}&connectAttempt={1}&connectUrl={2}" -f [uri]::EscapeDataString($ConnectAccount), [uri]::EscapeDataString($ConnectAttempt), [uri]::EscapeDataString($ConnectUrl)
    $separator = if ($url.Contains('#')) { '&' } else { '#' }
    $url = "$url$separator$fragment"
}
Write-Output $url
if (-not $NoBrowser) {
    Start-Process $url
}
