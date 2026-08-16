[CmdletBinding()]
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]] $ArgumentList
)

$ErrorActionPreference = "Stop"
$installRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$configPath = Join-Path $installRoot "router-config.json"

if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) {
    throw "Router configuration is missing; rerun install.ps1."
}

$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
foreach ($required in @("codexBackendExecutable", "muxExecutable", "stateRoot", "primaryCodexHome")) {
    if ([string]::IsNullOrWhiteSpace([string]$config.$required)) {
        throw "Router configuration is missing '$required'; rerun install.ps1."
    }
}

foreach ($path in @($config.codexBackendExecutable, $config.muxExecutable)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Configured router path is missing: $path"
    }
}

$env:CODEX_MUX_REAL_CODEX = [string]$config.codexBackendExecutable
$env:CODEX_MUX_HOME = [string]$config.stateRoot
$env:CODEX_HOME = [string]$config.primaryCodexHome
$env:CODEX_SQLITE_HOME = [string]$config.primaryCodexHome
$launchArguments = @("-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled")
if ($ArgumentList -and $ArgumentList.Count -gt 0) {
    $launchArguments = $ArgumentList
}

& ([string]$config.muxExecutable) @launchArguments
exit $LASTEXITCODE
