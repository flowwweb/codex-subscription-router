$ErrorActionPreference = "Stop"

$root = (Resolve-Path (Join-Path $PSScriptRoot ".."))
$files = @(
    (Join-Path $root "install.ps1"),
    (Join-Path $root "scripts\windows\launch-router.ps1"),
    (Join-Path $root "scripts\windows\start-router.ps1"),
    (Join-Path $root "scripts\windows\open-dashboard.ps1")
    (Join-Path $root "scripts\windows\verify-installed-router.ps1")
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
$dashboardLauncher = Get-Content -LiteralPath (Join-Path $root "scripts\windows\open-dashboard.cmd") -Raw
if ($dashboardLauncher -notmatch "open-dashboard\.ps1") {
    throw "Windows dashboard launcher does not invoke open-dashboard.ps1"
}

$installer = Get-Content -LiteralPath (Join-Path $root "install.ps1") -Raw
$launcherScript = Get-Content -LiteralPath (Join-Path $root "scripts\windows\launch-router.ps1") -Raw
$startScript = Get-Content -LiteralPath (Join-Path $root "scripts\windows\start-router.ps1") -Raw
$dashboardScript = Get-Content -LiteralPath (Join-Path $root "scripts\windows\open-dashboard.ps1") -Raw
$contracts = "$installer`n$launcherScript`n$startScript`n$dashboardScript"
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
    "ReparsePoint",
    "runtime.json",
    "dashboard-url",
    "Schedule.Service",
    "buildId",
    "muxSha256",
    "WindowStyle Hidden"
)) {
    if ($contracts -notmatch [regex]::Escape($required)) {
        throw "Windows installer is missing required contract: $required"
    }
}

$markerDecision = $installer.IndexOf('$primaryRequiresAclMigration')
$markerWrite = $installer.IndexOf('Set-Content -LiteralPath $primaryAclMarker')
if ($markerDecision -lt 0 -or $markerWrite -le $markerDecision) {
    throw "Windows installer must write the primary ACL completion marker only after deciding migration is required"
}
if ($installer -notmatch '-not \(Test-Path -LiteralPath \$primaryAclMarker -PathType Leaf\)') {
    throw "Windows installer must resume primary ACL migration when its completion marker is absent"
}

Write-Output "Windows installer and launcher syntax passed"
