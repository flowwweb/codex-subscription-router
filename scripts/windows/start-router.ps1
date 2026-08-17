[CmdletBinding()]
param([int] $ReadyTimeoutSeconds = 30, [string] $InstallRoot)

$ErrorActionPreference = "Stop"
$installRoot = if ([string]::IsNullOrWhiteSpace($InstallRoot)) { Split-Path -Parent $MyInvocation.MyCommand.Path } else { $InstallRoot }
$configPath = Join-Path $installRoot "router-config.json"

function Fail([string] $Message) { throw "Router start failed: $Message" }

if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) {
    Fail "configuration is missing; rerun install.ps1"
}
$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
foreach ($required in @("codexBackendExecutable", "codexBackendSha256", "muxExecutable", "muxSha256", "stateRoot", "primaryCodexHome", "buildId")) {
    if ([string]::IsNullOrWhiteSpace([string]$config.$required)) {
        Fail "configuration is missing '$required'; rerun install.ps1"
    }
}
foreach ($path in @($config.codexBackendExecutable, $config.muxExecutable)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { Fail "configured executable is missing: $path" }
}
if ((Get-FileHash -LiteralPath $config.codexBackendExecutable -Algorithm SHA256).Hash -ne $config.codexBackendSha256) {
    Fail "the Codex backend changed; rerun install.ps1 to verify compatibility"
}
if ((Get-FileHash -LiteralPath $config.muxExecutable -Algorithm SHA256).Hash -ne $config.muxSha256) {
    Fail "the installed router binary failed its integrity check"
}

function Get-RuntimeReceipt {
    $path = Join-Path $config.stateRoot "runtime.json"
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { return $null }
    try { return Get-Content -LiteralPath $path -Raw | ConvertFrom-Json } catch { return $null }
}

function Test-Runtime([object] $Receipt) {
    if (-not $Receipt -or [string]::IsNullOrWhiteSpace([string]$Receipt.address) -or [string]::IsNullOrWhiteSpace([string]$Receipt.instance)) { return $false }
    try {
        $result = Invoke-RestMethod -Method Get -Uri ("http://{0}/v1/runtime/ready" -f $Receipt.address) -Headers @{ "X-Codex-Mux-Instance" = [string]$Receipt.instance } -TimeoutSec 3
        return ([string]$result.instance -eq [string]$Receipt.instance -and [string]$result.build -eq [string]$config.buildId)
    } catch { return $false }
}

$existing = Get-RuntimeReceipt
if (Test-Runtime $existing) {
    Write-Output ($existing | ConvertTo-Json -Compress)
    exit 0
}
if ($existing -and -not [string]::IsNullOrWhiteSpace([string]$existing.address) -and -not [string]::IsNullOrWhiteSpace([string]$existing.instance)) {
    $previousPid = [int]$existing.pid
    $recordedProcess = if ($previousPid -gt 0) { Get-CimInstance Win32_Process -Filter "ProcessId = $previousPid" -ErrorAction SilentlyContinue } else { $null }
    if (-not $recordedProcess) {
        $staleReceiptPath = Join-Path $config.stateRoot "runtime.json"
        Remove-Item -LiteralPath $staleReceiptPath -Force -ErrorAction Stop
        $existing = $null
    } else {
        $recordedPath = [string]$recordedProcess.ExecutablePath
        $versionsRoot = [System.IO.Path]::GetFullPath((Join-Path $installRoot "versions")).TrimEnd('\') + '\'
        $isInstalledMux = -not [string]::IsNullOrWhiteSpace($recordedPath) -and
            [System.IO.Path]::GetFullPath($recordedPath).StartsWith($versionsRoot, [System.StringComparison]::OrdinalIgnoreCase) -and
            (Split-Path -Leaf $recordedPath) -eq "codex-mux.exe"
        if (-not $isInstalledMux) {
            Remove-Item -LiteralPath (Join-Path $config.stateRoot "runtime.json") -Force -ErrorAction Stop
            $existing = $null
        }
    }
}
if ($existing -and -not [string]::IsNullOrWhiteSpace([string]$existing.address) -and -not [string]::IsNullOrWhiteSpace([string]$existing.instance)) {
    $previousPid = [int]$existing.pid
    try {
        Invoke-WebRequest -UseBasicParsing -Method Post -Uri ("http://{0}/v1/runtime/shutdown" -f $existing.address) -Headers @{ "X-Codex-Mux-Instance" = [string]$existing.instance } -TimeoutSec 3 | Out-Null
    } catch {
        Fail "could not stop the previous daemon: $($_.Exception.Message)"
    }
    if ($previousPid -gt 0) {
        Wait-Process -Id $previousPid -Timeout 10 -ErrorAction SilentlyContinue
        if (Get-Process -Id $previousPid -ErrorAction SilentlyContinue) {
            Fail "previous daemon PID $previousPid did not stop within 10 seconds"
        }
    }
}

if ($env:CODEX_MUX_ACCEPTANCE_TEST -eq "simulate-readiness-failure") {
    Fail "simulated daemon readiness failure after previous-owner shutdown"
}

$env:CODEX_MUX_REAL_CODEX = [string]$config.codexBackendExecutable
$env:CODEX_MUX_HOME = [string]$config.stateRoot
$env:CODEX_HOME = [string]$config.primaryCodexHome
$env:CODEX_SQLITE_HOME = [string]$config.primaryCodexHome
$process = Start-Process -FilePath $config.muxExecutable -ArgumentList @("daemon", "-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled") -WorkingDirectory $installRoot -PassThru -WindowStyle Hidden

$deadline = [DateTime]::UtcNow.AddSeconds($ReadyTimeoutSeconds)
do {
    if ($process.HasExited) { Fail "daemon exited with code $($process.ExitCode) before readiness" }
    Start-Sleep -Milliseconds 200
    $receipt = Get-RuntimeReceipt
    if ($receipt -and [int]$receipt.pid -eq $process.Id -and (Test-Runtime $receipt)) {
        Write-Output ($receipt | ConvertTo-Json -Compress)
        exit 0
    }
} while ([DateTime]::UtcNow -lt $deadline)

Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
Fail "exact installed daemon did not become ready within $ReadyTimeoutSeconds seconds"
