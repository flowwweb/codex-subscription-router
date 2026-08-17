[CmdletBinding()]
param(
    [string] $InstallRoot = (Join-Path $env:LOCALAPPDATA "Codex Subscription Router"),
    [switch] $ExerciseCrashRecovery,
    [switch] $ExerciseIntegrityFailure
)

$ErrorActionPreference = "Stop"
$configPath = Join-Path $InstallRoot "router-config.json"
if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) { throw "Installed router configuration is missing." }
$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
$receiptPath = Join-Path $config.stateRoot "runtime.json"

function Get-Receipt { Get-Content -LiteralPath $receiptPath -Raw | ConvertFrom-Json }
function Assert-ExactRuntime {
    $receipt = Get-Receipt
    if ([string]$receipt.build -ne [string]$config.buildId) { throw "Runtime build does not match installed configuration." }
    $process = Get-CimInstance Win32_Process -Filter "ProcessId = $([int]$receipt.pid)"
    if (-not $process -or [string]$process.ExecutablePath -ne [string]$config.muxExecutable) { throw "Runtime PID is not the exact installed mux." }
    $instanceHeader = @{ "X-Codex-Mux-Instance" = [string]$receipt.instance }
    $ready = Invoke-RestMethod -Uri ("http://{0}/v1/runtime/ready" -f $receipt.address) -Headers $instanceHeader -TimeoutSec 3
    if ([string]$ready.instance -ne [string]$receipt.instance) { throw "Runtime readiness rejected exact identity." }
    return $receipt
}

$first = Assert-ExactRuntime
if ((Get-FileHash -LiteralPath $config.muxExecutable -Algorithm SHA256).Hash -ne [string]$config.muxSha256) { throw "Mux hash mismatch." }
if ((Get-FileHash -LiteralPath $config.codexBackendExecutable -Algorithm SHA256).Hash -ne [string]$config.codexBackendSha256) { throw "Backend hash mismatch." }

$task = Get-ScheduledTask -TaskName "Codex Subscription Router"
if ($task.Settings.DisallowStartIfOnBatteries -or $task.Settings.StopIfGoingOnBatteries) { throw "Scheduled task is not battery-safe." }

$acl = Get-Acl -LiteralPath $receiptPath
if (-not $acl.AreAccessRulesProtected) { throw "Runtime receipt DACL is not protected." }
$allowed = @($acl.Access | ForEach-Object { $_.IdentityReference.Value })
if (@($allowed | Where-Object { $_ -notin @([System.Security.Principal.WindowsIdentity]::GetCurrent().Name, "NT AUTHORITY\SYSTEM") }).Count -gt 0) {
    throw "Runtime receipt grants an unexpected principal."
}

if ($ExerciseIntegrityFailure) {
    $raw = Get-Content -LiteralPath $configPath -Raw
    try {
        $changed = $raw | ConvertFrom-Json
        $changed.codexBackendSha256 = "0" * 64
        $changed | ConvertTo-Json | Set-Content -LiteralPath $configPath -Encoding UTF8
        $failed = $false
        try { & ([string]$config.startScript) -InstallRoot $InstallRoot | Out-Null } catch { $failed = $true }
        if (-not $failed) { throw "Launcher accepted a deliberately invalid backend hash." }
    } finally {
        $raw | Set-Content -LiteralPath $configPath -Encoding UTF8
    }
    $config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    [void](Assert-ExactRuntime)
}

if ($ExerciseCrashRecovery) {
    Stop-Process -Id ([int]$first.pid) -Force
    Wait-Process -Id ([int]$first.pid) -Timeout 10 -ErrorAction SilentlyContinue
    if (-not (Test-Path -LiteralPath $receiptPath -PathType Leaf)) { throw "Crash exercise did not leave a stale receipt." }
    $recovered = & ([string]$config.startScript) -InstallRoot $InstallRoot | Select-Object -Last 1 | ConvertFrom-Json
    if ([int]$recovered.pid -eq [int]$first.pid) { throw "Crash recovery did not start a replacement PID." }
    $first = Assert-ExactRuntime
}

Start-ScheduledTask -TaskName "Codex Subscription Router"
Start-Sleep -Seconds 2
$final = Assert-ExactRuntime
$muxProcesses = @(Get-CimInstance Win32_Process -Filter "Name = 'codex-mux.exe'")
if ($muxProcesses.Count -ne 1) { throw "Scheduled start left $($muxProcesses.Count) mux processes." }

[pscustomobject]@{
    verified = $true
    build = [string]$final.build
    pid = [int]$final.pid
    processCount = $muxProcesses.Count
    crashRecoveryExercised = [bool]$ExerciseCrashRecovery
    integrityFailureExercised = [bool]$ExerciseIntegrityFailure
    batterySafe = $true
    aclProtected = $true
} | ConvertTo-Json
