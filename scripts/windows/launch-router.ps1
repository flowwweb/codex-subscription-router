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

function Get-NormalizedPath([string] $Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw "Router configuration contains an empty path."
    }
    try {
        $full = [System.IO.Path]::GetFullPath($Path)
        if ($full.Length -gt 3) {
            return $full.TrimEnd([char[]]@('\', '/'))
        }
        return $full
    } catch {
        throw "Router configuration contains an invalid path '$Path': $($_.Exception.Message)"
    }
}

function Test-PathWithin([string] $Path, [string] $Root) {
    $normalizedPath = Get-NormalizedPath $Path
    $normalizedRoot = Get-NormalizedPath $Root
    if ($normalizedPath.Equals($normalizedRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        return $true
    }
    $prefix = $normalizedRoot
    if (-not $prefix.EndsWith('\')) {
        $prefix += '\'
    }
    return $normalizedPath.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)
}

$normalizedInstallRoot = Get-NormalizedPath $installRoot
$routerStatePaths = @($config.stateRoot, $config.primaryCodexHome)
foreach ($statePath in $routerStatePaths) {
    if (-not (Test-PathWithin $statePath $normalizedInstallRoot) -or
        (Test-PathWithin $statePath (Join-Path $env:USERPROFILE '.codex'))) {
        throw "Router state path must remain under the launcher directory and outside %USERPROFILE%\\.codex: $statePath"
    }
    if (Test-Path -LiteralPath $statePath -PathType Leaf) {
        throw "Router state path is a file, not a private directory: $statePath"
    }
    $stateItem = Get-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
    if ($stateItem -and ($stateItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
        throw "Router state path cannot be a reparse point: $statePath"
    }
}
if ((Get-NormalizedPath $config.stateRoot).Equals((Get-NormalizedPath $config.primaryCodexHome), [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Router stateRoot and primaryCodexHome must be separate directories."
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
