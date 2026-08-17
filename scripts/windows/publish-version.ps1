param(
    [Parameter(Mandatory = $true)][string] $StagingRoot,
    [Parameter(Mandatory = $true)][string] $VersionRoot,
    [Parameter(Mandatory = $true)][string] $ScriptsRoot
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path -LiteralPath (Join-Path $StagingRoot "codex-mux.exe") -PathType Leaf)) {
    throw "staged router executable is missing"
}
if (Test-Path -LiteralPath $VersionRoot) {
    throw "version directory already exists: $VersionRoot"
}

$requiredScripts = @("start-router.ps1", "launch-router.ps1", "open-dashboard.ps1", "connect-account.ps1")
foreach ($name in $requiredScripts) {
    if ($env:CODEX_MUX_ACCEPTANCE_TEST -eq "simulate-version-copy-failure" -and $name -eq "launch-router.ps1") {
        throw "simulated version script copy failure"
    }
    Copy-Item -LiteralPath (Join-Path $ScriptsRoot $name) -Destination (Join-Path $StagingRoot $name)
}

foreach ($name in @("codex-mux.exe") + $requiredScripts) {
    if (-not (Test-Path -LiteralPath (Join-Path $StagingRoot $name) -PathType Leaf)) {
        throw "staged version is incomplete: $name"
    }
}

[System.IO.Directory]::Move(
    [System.IO.Path]::GetFullPath($StagingRoot),
    [System.IO.Path]::GetFullPath($VersionRoot)
)
