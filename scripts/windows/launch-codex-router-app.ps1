[CmdletBinding()]
param([string] $InstallRoot)

$ErrorActionPreference = "Stop"
$installRoot = if ([string]::IsNullOrWhiteSpace($InstallRoot)) { Split-Path -Parent $MyInvocation.MyCommand.Path } else { $InstallRoot }
$configPath = Join-Path $installRoot "router-config.json"
if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) { throw "Router configuration is missing; rerun install.ps1." }
$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
foreach ($required in @("routerAppExecutable", "routerAppAsarSha256", "routerAppUserData", "stateRoot", "primaryCodexHome")) {
    if ([string]::IsNullOrWhiteSpace([string]$config.$required)) { throw "Router app configuration is missing '$required'; rerun install.ps1." }
}
$asarPath = Join-Path (Split-Path -Parent $config.routerAppExecutable) "resources\app.asar"
if (-not (Test-Path -LiteralPath $config.routerAppExecutable -PathType Leaf) -or -not (Test-Path -LiteralPath $asarPath -PathType Leaf)) {
    throw "FLOW app files are missing; rerun install.ps1."
}
if ((Get-FileHash -LiteralPath $asarPath -Algorithm SHA256).Hash -ne [string]$config.routerAppAsarSha256) {
    throw "FLOW app failed its integrity check; rerun install.ps1."
}
$start = New-Object System.Diagnostics.ProcessStartInfo
$start.FileName = [string]$config.routerAppExecutable
$start.WorkingDirectory = Split-Path -Parent $config.routerAppExecutable
$start.UseShellExecute = $false
$start.Environment["CODEX_ELECTRON_USER_DATA_PATH"] = [string]$config.routerAppUserData
$start.Environment["CODEX_MUX_HOME"] = [string]$config.stateRoot
$start.Environment["CODEX_HOME"] = [string]$config.primaryCodexHome
$start.Environment["CODEX_SQLITE_HOME"] = [string]$config.primaryCodexHome
$process = [System.Diagnostics.Process]::Start($start)
if (-not $process) { throw "FLOW app did not start." }
[pscustomobject]@{ started = $true; pid = $process.Id; executable = [string]$config.routerAppExecutable } | ConvertTo-Json -Compress
