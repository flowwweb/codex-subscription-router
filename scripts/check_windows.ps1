$ErrorActionPreference = "Stop"

$root = (Resolve-Path (Join-Path $PSScriptRoot ".."))
$files = @(
    (Join-Path $root "install.ps1"),
    (Join-Path $root "scripts\windows\launch-router.ps1")
)

foreach ($file in $files) {
    $tokens = $null
    $errors = $null
    [System.Management.Automation.Language.Parser]::ParseFile($file, [ref]$tokens, [ref]$errors) | Out-Null
    if ($errors.Count -gt 0) {
        throw "PowerShell syntax errors in ${file}: $($errors -join '; ')"
    }
}

$launcher = Get-Content -LiteralPath (Join-Path $root "scripts\windows\launch-router.cmd") -Raw
if ($launcher -notmatch "launch-router\.ps1") {
    throw "Windows command launcher does not invoke launch-router.ps1"
}

$installer = Get-Content -LiteralPath (Join-Path $root "install.ps1") -Raw
$launcherScript = Get-Content -LiteralPath (Join-Path $root "scripts\windows\launch-router.ps1") -Raw
$contracts = "$installer`n$launcherScript"
foreach ($required in @(
    "codexBackendExecutable",
    "CODEX_MUX_REAL_CODEX",
    "CODEX_MUX_HOME",
    "primaryCodexHome",
    "Get-FileHash",
    "Set-PrivateAclEntry",
    "AreAccessRulesProtected",
    "Assert-NoPathCollision",
    "source checkout",
    "router install",
    "Test-PathWithin",
    "%USERPROFILE%\\.codex",
    "ReparsePoint"
)) {
    if ($contracts -notmatch [regex]::Escape($required)) {
        throw "Windows installer is missing required contract: $required"
    }
}

Write-Output "Windows installer and launcher syntax passed"
