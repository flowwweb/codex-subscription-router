[CmdletBinding()]
param(
    [ValidateSet("Status", "Connect")]
    [string] $Action = "Status",
    [switch] $NewAccount,
    [switch] $NoOpen,
    [switch] $NoWait,
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$')]
    [string] $AccountId,
    [string] $InstallRoot
)

$ErrorActionPreference = "Stop"
$installRoot = if ([string]::IsNullOrWhiteSpace($InstallRoot)) { Split-Path -Parent $MyInvocation.MyCommand.Path } else { $InstallRoot }
$configPath = Join-Path $installRoot "router-config.json"

function Fail([string] $Message) { throw "Codex Router account command failed: $Message" }

if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) {
    Fail "configuration is missing; rerun install.ps1"
}
$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
foreach ($required in @("muxExecutable", "muxSha256", "startScript", "stateRoot")) {
    if ([string]::IsNullOrWhiteSpace([string]$config.$required)) {
        Fail "configuration is missing '$required'; rerun install.ps1"
    }
}
if (-not (Test-Path -LiteralPath $config.startScript -PathType Leaf)) {
    Fail "the installed start script is missing; rerun install.ps1"
}

if ($Action -eq "Status" -and ($NewAccount -or $NoOpen -or $NoWait -or -not [string]::IsNullOrWhiteSpace($AccountId))) {
    Fail "account connection options require -Action Connect"
}
if ($NewAccount -and -not [string]::IsNullOrWhiteSpace($AccountId)) {
    Fail "use either -NewAccount or -AccountId, not both"
}

& ([string]$config.startScript) -InstallRoot $installRoot | Out-Null
if (-not (Test-Path -LiteralPath $config.muxExecutable -PathType Leaf)) {
    Fail "the installed router executable is missing; rerun install.ps1"
}
if ((Get-FileHash -LiteralPath $config.muxExecutable -Algorithm SHA256).Hash -ne [string]$config.muxSha256) {
    Fail "the installed router binary failed its integrity check"
}

$env:CODEX_MUX_HOME = [string]$config.stateRoot
$arguments = if ($Action -eq "Status") {
    @("status", "--json")
} else {
    @("connect-account", "--json")
}
if ($Action -eq "Connect") {
    if ($NoWait) { $arguments += "--wait=false" } else { $arguments += "--wait=true" }
    if ($NoOpen) { $arguments += "--open=false" } else { $arguments += "--open=true" }
    if ($NewAccount) { $arguments += "--new-account" }
    if (-not [string]::IsNullOrWhiteSpace($AccountId)) { $arguments += @("--account-id", $AccountId) }
}

& ([string]$config.muxExecutable) @arguments
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
