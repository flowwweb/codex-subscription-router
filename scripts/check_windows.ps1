$ErrorActionPreference = "Stop"

$root = (Resolve-Path (Join-Path $PSScriptRoot ".."))
$files = @(
    (Join-Path $root "install.ps1"),
    (Join-Path $root "scripts\windows\launch-router.ps1"),
    (Join-Path $root "scripts\windows\start-router.ps1"),
    (Join-Path $root "scripts\windows\open-dashboard.ps1")
    (Join-Path $root "scripts\windows\connect-account.ps1")
    (Join-Path $root "scripts\windows\publish-version.ps1")
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
$verifyScript = Get-Content -LiteralPath (Join-Path $root "scripts\windows\verify-installed-router.ps1") -Raw
if ($verifyScript -notmatch "CommandLine -match '\(\?:\^\|\\s\)daemon") {
    throw "Installed verifier does not distinguish the daemon from account connection helpers"
}
$connectScript = Get-Content -LiteralPath (Join-Path $root "scripts\windows\connect-account.ps1") -Raw
$accountClient = Get-Content -LiteralPath (Join-Path $root "cmd\codex-mux\account_client.go") -Raw
$routerSkill = Get-Content -LiteralPath (Join-Path $root "plugins\codex-router\skills\codex-router\SKILL.md") -Raw
$contracts = "$installer`n$launcherScript`n$startScript`n$dashboardScript`n$connectScript"
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
    "Connect Codex Router Account.ps1"
    "connectScript"
    "connectLauncher"
    "connect-account"
    "--new-account"
    "--account-id"
)) {
    if ($contracts -notmatch [regex]::Escape($required)) {
        throw "Windows installer is missing required contract: $required"
    }
}

foreach ($required in @(
    '[ValidateSet("Status", "Connect")]',
    "ValidatePattern",
    'if ($NoWait)',
    'if ($NoOpen)',
    'if ($NewAccount)',
    '$config.startScript',
    '$config.muxExecutable',
    '$config.muxSha256',
    '$env:CODEX_MUX_HOME'
)) {
    if ($connectScript -notmatch [regex]::Escape($required)) {
        throw "Windows account launcher is missing required contract: $required"
    }
}
foreach ($forbidden in @("control-token", "dashboard-url", "Invoke-RestMethod", "Invoke-WebRequest")) {
    if ($connectScript -match [regex]::Escape($forbidden)) {
        throw "Windows account launcher must not use protected control or URL contract: $forbidden"
    }
}
if ($connectScript -notmatch [regex]::Escape('exit $LASTEXITCODE') -or $connectScript -match 'router command exited') {
    throw "Windows account launcher must preserve one structured router failure without duplicating it"
}
if ($connectScript -notmatch '& \(\[string\]\$config\.muxExecutable\) @arguments') {
    throw "Windows account launcher must invoke the exact configured router with an argument array"
}

foreach ($crossSurface in @(
    @{ Launcher = '"status", "--json"'; Client = '"status"'; Skill = '-Action Status' },
    @{ Launcher = '"connect-account", "--json"'; Client = '"connect-account"'; Skill = '-Action Connect' },
    @{ Launcher = '"--new-account"'; Client = '"new-account"'; Skill = '-NewAccount' },
    @{ Launcher = '"--account-id"'; Client = '"account-id"'; Skill = '-AccountId' },
    @{ Launcher = '"--wait=false"'; Client = '"wait"'; Skill = 'terminal event' },
    @{ Launcher = '"--open=false"'; Client = '"open"'; Skill = 'verification URLs' }
)) {
    if ($connectScript -notmatch [regex]::Escape($crossSurface.Launcher) -or
        $accountClient -notmatch [regex]::Escape($crossSurface.Client) -or
        $routerSkill -notmatch [regex]::Escape($crossSurface.Skill)) {
        throw "Codex plugin, Windows launcher, and Go client contract drifted: $($crossSurface.Launcher)"
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

$publishVersion = Join-Path $root "scripts\windows\publish-version.ps1"
$acceptanceRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("codex-router-publish-{0}" -f [Guid]::NewGuid().ToString("N"))
$scriptsRoot = Join-Path $root "scripts\windows"
$failedStaging = Join-Path $acceptanceRoot ".staging-failed"
$retryStaging = Join-Path $acceptanceRoot ".staging-retry"
$collisionStaging = Join-Path $acceptanceRoot ".staging-collision"
$versionRoot = Join-Path $acceptanceRoot "version"
New-Item -ItemType Directory -Force -Path $failedStaging,$retryStaging,$collisionStaging | Out-Null
"fake mux" | Set-Content -LiteralPath (Join-Path $failedStaging "codex-mux.exe")
"fake mux" | Set-Content -LiteralPath (Join-Path $retryStaging "codex-mux.exe")
"losing mux" | Set-Content -LiteralPath (Join-Path $collisionStaging "codex-mux.exe")
try {
    $env:CODEX_MUX_ACCEPTANCE_TEST = "simulate-version-copy-failure"
    try { & $publishVersion -StagingRoot $failedStaging -VersionRoot $versionRoot -ScriptsRoot $scriptsRoot; throw "simulated copy failure unexpectedly succeeded" }
    catch { if ($_.Exception.Message -eq "simulated copy failure unexpectedly succeeded") { throw } }
    if (Test-Path -LiteralPath $versionRoot) { throw "copy failure published a partial version" }
    Remove-Item Env:CODEX_MUX_ACCEPTANCE_TEST -ErrorAction SilentlyContinue
    & $publishVersion -StagingRoot $retryStaging -VersionRoot $versionRoot -ScriptsRoot $scriptsRoot
    foreach ($name in @("codex-mux.exe", "start-router.ps1", "launch-router.ps1", "open-dashboard.ps1", "connect-account.ps1")) {
        if (-not (Test-Path -LiteralPath (Join-Path $versionRoot $name) -PathType Leaf)) { throw "retry published an incomplete version: $name" }
    }
    $winnerBytes = [Convert]::ToBase64String([System.IO.File]::ReadAllBytes((Join-Path $versionRoot "codex-mux.exe")))
    try { & $publishVersion -StagingRoot $collisionStaging -VersionRoot $versionRoot -ScriptsRoot $scriptsRoot; throw "colliding publish unexpectedly succeeded" }
    catch { if ($_.Exception.Message -eq "colliding publish unexpectedly succeeded") { throw } }
    if (-not (Test-Path -LiteralPath $collisionStaging -PathType Container)) { throw "colliding publisher lost its staging directory" }
    if (Test-Path -LiteralPath (Join-Path $versionRoot ".staging-collision")) { throw "colliding publish nested content under the winner" }
    if ([Convert]::ToBase64String([System.IO.File]::ReadAllBytes((Join-Path $versionRoot "codex-mux.exe"))) -ne $winnerBytes) { throw "colliding publish modified the winner" }
} finally {
    Remove-Item Env:CODEX_MUX_ACCEPTANCE_TEST -ErrorAction SilentlyContinue
    $resolvedAcceptanceRoot = [System.IO.Path]::GetFullPath($acceptanceRoot)
    $resolvedTempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    if ($resolvedAcceptanceRoot.StartsWith($resolvedTempRoot, [System.StringComparison]::OrdinalIgnoreCase) -and (Split-Path -Leaf $resolvedAcceptanceRoot) -like "codex-router-publish-*") {
        Remove-Item -LiteralPath $resolvedAcceptanceRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Write-Output "Windows installer and launcher syntax passed"
