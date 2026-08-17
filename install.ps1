[CmdletBinding()]
param(
    [string] $OfficialExecutable,
    [string] $CodexExecutable,
    [switch] $NoLaunch
)

$ErrorActionPreference = "Stop"
$installRoot = Join-Path $env:LOCALAPPDATA "Codex Subscription Router"
$sourceRoot = Split-Path -Parent $MyInvocation.MyCommand.Path

function Fail([string] $Message) {
    throw "Install failed: $Message"
}

function Resolve-OfficialExecutable {
    param([string] $ExplicitPath)

    if (-not [string]::IsNullOrWhiteSpace($ExplicitPath)) {
        $resolved = (Resolve-Path -LiteralPath $ExplicitPath -ErrorAction SilentlyContinue).Path
        if ($resolved -and (Test-Path -LiteralPath $resolved -PathType Leaf)) {
            return $resolved
        }
        Fail "the supplied official executable does not exist: $ExplicitPath"
    }

    $running = Get-Process -Name ChatGPT,Codex -ErrorAction SilentlyContinue |
        Where-Object { -not [string]::IsNullOrWhiteSpace($_.Path) -and (Split-Path -Leaf $_.Path) -eq "ChatGPT.exe" } |
        Select-Object -First 1
    if ($running) {
        return $running.Path
    }

    $package = Get-AppxPackage -Name "OpenAI.Codex" -ErrorAction SilentlyContinue |
        Sort-Object Version -Descending |
        Select-Object -First 1
    if ($package) {
        $candidate = Join-Path $package.InstallLocation "app\ChatGPT.exe"
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            return $candidate
        }
    }

    $windowsApps = Join-Path ${env:ProgramFiles} "WindowsApps"
    $candidates = @()
    if (Test-Path -LiteralPath $windowsApps -PathType Container) {
        $candidates = Get-ChildItem -LiteralPath $windowsApps -Directory -Filter "OpenAI.Codex_*" -ErrorAction SilentlyContinue |
            ForEach-Object { Join-Path $_.FullName "app\ChatGPT.exe" } |
            Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
            Sort-Object -Descending
    }
    if ($candidates.Count -gt 0) {
        return $candidates[0]
    }

    Fail "could not find the installed official Windows Codex app; pass -OfficialExecutable C:\\path\\to\\ChatGPT.exe"
}

function Get-AbsolutePath([string] $Path) {
    return (Resolve-Path -LiteralPath $Path).Path
}

function Get-NormalizedPath([string] $Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) {
        Fail "cannot normalize an empty path"
    }
    try {
        $full = [System.IO.Path]::GetFullPath($Path)
        if ($full.Length -gt 3) {
            return $full.TrimEnd([char[]]@('\', '/'))
        }
        return $full
    } catch {
        Fail "could not normalize path '$Path': $($_.Exception.Message)"
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

function Assert-NoPathCollision([string] $LeftLabel, [string] $Left, [string] $RightLabel, [string] $Right) {
    if ((Test-PathWithin $Left $Right) -or (Test-PathWithin $Right $Left)) {
        Fail "$LeftLabel and $RightLabel overlap; choose paths with separate ownership"
    }
}

function Resolve-CodexBackend {
    param([string] $ExplicitPath)

    if (-not [string]::IsNullOrWhiteSpace($ExplicitPath)) {
        $resolved = (Resolve-Path -LiteralPath $ExplicitPath -ErrorAction SilentlyContinue).Path
        if ($resolved -and (Test-Path -LiteralPath $resolved -PathType Leaf)) {
            return $resolved
        }
        Fail "the supplied Codex backend does not exist: $ExplicitPath"
    }

    $running = Get-CimInstance Win32_Process -Filter "Name = 'codex.exe'" -ErrorAction SilentlyContinue |
        Where-Object {
            $_.ExecutablePath -and
            $_.ExecutablePath -like (Join-Path $env:LOCALAPPDATA 'OpenAI\Codex\bin\*\codex.exe')
        } |
        Select-Object -First 1
    if ($running) {
        return $running.ExecutablePath
    }

    $userBackendRoot = Join-Path $env:LOCALAPPDATA 'OpenAI\Codex\bin'
    $userBackend = Get-ChildItem -LiteralPath $userBackendRoot -Directory -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTime -Descending |
        ForEach-Object { Join-Path $_.FullName 'codex.exe' } |
        Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
        Select-Object -First 1
    if ($userBackend) {
        return $userBackend
    }

    Fail "could not find a runnable Windows Codex backend; pass -CodexExecutable C:\path\to\codex.exe"
}

function Set-PrivateAclEntry([string] $Path, [bool] $IsDirectory) {
    $current = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
    $system = New-Object System.Security.Principal.SecurityIdentifier("S-1-5-18")
    $item = if ($IsDirectory) {
        New-Object System.IO.DirectoryInfo($Path)
    } else {
        New-Object System.IO.FileInfo($Path)
    }
    $acl = $item.GetAccessControl()
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($identity in @($acl.Access | ForEach-Object IdentityReference | Select-Object -Unique)) {
        $acl.PurgeAccessRules($identity)
    }
    $inheritance = [System.Security.AccessControl.InheritanceFlags]::None
    if ($IsDirectory) {
        $inheritance = [System.Security.AccessControl.InheritanceFlags]"ContainerInherit, ObjectInherit"
    }
    foreach ($sid in @($current, $system)) {
        $rule = New-Object System.Security.AccessControl.FileSystemAccessRule(
            $sid,
            [System.Security.AccessControl.FileSystemRights]::FullControl,
            $inheritance,
            [System.Security.AccessControl.PropagationFlags]::None,
            [System.Security.AccessControl.AccessControlType]::Allow
        )
        [void]$acl.AddAccessRule($rule)
    }
    $acl.SetOwner($current)
    $item.SetAccessControl($acl)

    $verified = $item.GetAccessControl()
    $allowed = @($current.Value, $system.Value)
    $unexpected = @($verified.Access | Where-Object {
        $sid = $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
        $sid -notin $allowed -or $_.AccessControlType -ne [System.Security.AccessControl.AccessControlType]::Allow
    })
    if (-not $verified.AreAccessRulesProtected -or $unexpected.Count -gt 0) {
        Fail "effective ACL verification failed: $Path"
    }
}

function Set-PrivateStateAcl([string] $Path) {
    Set-PrivateAclEntry -Path $Path -IsDirectory $true
    foreach ($item in @(Get-ChildItem -LiteralPath $Path -Recurse -Force -ErrorAction SilentlyContinue)) {
        if (Test-Path -LiteralPath $item.FullName) {
            Set-PrivateAclEntry -Path $item.FullName -IsDirectory $item.PSIsContainer
        }
    }
}

function Test-PrivateAclEntry([string] $Path) {
    try {
        $current = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        $allowed = @($current, "S-1-5-18")
        $item = New-Object System.IO.DirectoryInfo($Path)
        $acl = $item.GetAccessControl()
        $unexpected = @($acl.Access | Where-Object {
            $sid = $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
            $sid -notin $allowed -or $_.AccessControlType -ne [System.Security.AccessControl.AccessControlType]::Allow
        })
        return $acl.AreAccessRulesProtected -and $unexpected.Count -eq 0
    } catch {
        return $false
    }
}

if ($env:OS -ne "Windows_NT") {
    Fail "this installer is for Windows only"
}
if (-not (Get-Command go.exe -ErrorAction SilentlyContinue)) {
    Fail "Go 1.26 or newer is required"
}

$normalizedSourceRoot = Get-NormalizedPath $sourceRoot
$normalizedInstallRoot = Get-NormalizedPath $installRoot
Assert-NoPathCollision "source checkout" $normalizedSourceRoot "router install" $normalizedInstallRoot

$official = Get-AbsolutePath (Resolve-OfficialExecutable -ExplicitPath $OfficialExecutable)
$officialRoot = Split-Path -Parent $official
$officialPackage = Get-AppxPackage -Name "OpenAI.Codex" -ErrorAction SilentlyContinue |
    Where-Object {
        $packageExecutable = Join-Path $_.InstallLocation "app\ChatGPT.exe"
        (Test-Path -LiteralPath $packageExecutable -PathType Leaf) -and
            (Get-NormalizedPath $packageExecutable) -eq (Get-NormalizedPath $official)
    } |
    Select-Object -First 1
$officialPackageVersion = if ($officialPackage) { [string]$officialPackage.Version } else { "unpackaged" }
$officialCodex = Join-Path $officialRoot "resources\codex.exe"
if (-not (Test-Path -LiteralPath $officialCodex -PathType Leaf)) {
    Fail "the official app does not contain resources\\codex.exe: $officialCodex"
}
$codexBackend = Get-AbsolutePath (Resolve-CodexBackend -ExplicitPath $CodexExecutable)

Assert-NoPathCollision "official app" (Get-NormalizedPath $officialRoot) "router install" $normalizedInstallRoot
Assert-NoPathCollision "Codex backend" (Get-NormalizedPath (Split-Path -Parent $codexBackend)) "router install" $normalizedInstallRoot

$asar = Join-Path $officialRoot "resources\app.asar"
$beforeAsarHash = (Get-FileHash -LiteralPath $asar -Algorithm SHA256).Hash
$beforeCodexHash = (Get-FileHash -LiteralPath $officialCodex -Algorithm SHA256).Hash
$beforeBackendHash = (Get-FileHash -LiteralPath $codexBackend -Algorithm SHA256).Hash
$stateRoot = Join-Path $installRoot "state"
$primaryCodexHome = Join-Path $installRoot "primary-codex-home"
$primaryAclMarker = Join-Path $primaryCodexHome ".private-acl-v2-complete"
$primaryRequiresAclMigration = (Test-Path -LiteralPath $primaryCodexHome -PathType Container) -and (
    -not (Test-PrivateAclEntry $primaryCodexHome) -or
    -not (Test-Path -LiteralPath $primaryAclMarker -PathType Leaf)
)
$versionsRoot = Join-Path $installRoot "versions"
$sourceRevision = $null
if (Get-Command git.exe -ErrorAction SilentlyContinue) {
    $sourceRevision = (& git.exe -C $sourceRoot rev-parse --short=12 HEAD 2>$null | Select-Object -First 1)
}
if ([string]::IsNullOrWhiteSpace($sourceRevision)) { $sourceRevision = "source" }
$buildId = "{0}-{1}" -f $sourceRevision.Trim(), [DateTime]::UtcNow.ToString("yyyyMMddHHmmss")
$stagingRoot = Join-Path $versionsRoot (".staging-{0}" -f [Guid]::NewGuid().ToString("N"))
$versionRoot = Join-Path $versionsRoot $buildId
$stagedMuxExecutable = Join-Path $stagingRoot "codex-mux.exe"
$muxExecutable = Join-Path $versionRoot "codex-mux.exe"
$startScript = Join-Path $versionRoot "start-router.ps1"
$launchScript = Join-Path $versionRoot "launch-router.ps1"
$dashboardScript = Join-Path $versionRoot "open-dashboard.ps1"

New-Item -ItemType Directory -Force -Path $installRoot,$stateRoot,$primaryCodexHome,$versionsRoot,$stagingRoot | Out-Null

Push-Location $sourceRoot
try {
    & go.exe build -trimpath -ldflags "-s -w -X main.buildID=$buildId" -o $stagedMuxExecutable ./cmd/codex-mux
    if ($LASTEXITCODE -ne 0) {
        Fail "Go multiplexer build failed"
    }
} finally {
    Pop-Location
}
if (Test-Path -LiteralPath $versionRoot) { Fail "version directory already exists: $versionRoot" }
Move-Item -LiteralPath $stagingRoot -Destination $versionRoot
$muxHash = (Get-FileHash -LiteralPath $muxExecutable -Algorithm SHA256).Hash
Copy-Item -LiteralPath (Join-Path $sourceRoot "scripts\windows\start-router.ps1") -Destination $startScript
Copy-Item -LiteralPath (Join-Path $sourceRoot "scripts\windows\launch-router.ps1") -Destination $launchScript
Copy-Item -LiteralPath (Join-Path $sourceRoot "scripts\windows\open-dashboard.ps1") -Destination $dashboardScript

$config = [ordered]@{
    schemaVersion = 2
    buildId = $buildId
    officialExecutable = $official
    officialCodexExecutable = $officialCodex
    codexBackendExecutable = $codexBackend
    muxExecutable = $muxExecutable
    muxSha256 = $muxHash
    startScript = $startScript
    launchScript = $launchScript
    dashboardScript = $dashboardScript
    stateRoot = $stateRoot
    primaryCodexHome = $primaryCodexHome
    officialVersion = (Get-Item -LiteralPath $official).VersionInfo.ProductVersion
    officialPackageVersion = $officialPackageVersion
    appAsarSha256 = $beforeAsarHash
    officialCodexSha256 = $beforeCodexHash
    codexBackendSha256 = $beforeBackendHash
}
$configPath = Join-Path $installRoot "router-config.json"
$previousConfig = $null
$previousConfigObject = $null
if (Test-Path -LiteralPath $configPath -PathType Leaf) {
    $previousConfig = Get-Content -LiteralPath $configPath -Raw
    try { $previousConfigObject = $previousConfig | ConvertFrom-Json } catch {}
}
if ($previousConfigObject -and [int]$previousConfigObject.schemaVersion -lt 2 -and -not [string]::IsNullOrWhiteSpace([string]$previousConfigObject.muxExecutable)) {
    $legacyMux = Get-NormalizedPath ([string]$previousConfigObject.muxExecutable)
    if ((Test-PathWithin $legacyMux $normalizedInstallRoot) -and (Split-Path -Leaf $legacyMux) -eq "codex-mux.exe") {
        Get-CimInstance Win32_Process -Filter "Name = 'codex-mux.exe'" -ErrorAction SilentlyContinue |
            Where-Object { $_.ExecutablePath -and (Get-NormalizedPath $_.ExecutablePath).Equals($legacyMux, [System.StringComparison]::OrdinalIgnoreCase) } |
            ForEach-Object {
                Stop-Process -Id $_.ProcessId -Force -ErrorAction Stop
                Wait-Process -Id $_.ProcessId -Timeout 10 -ErrorAction SilentlyContinue
            }
    }
}
$afterAsarHash = (Get-FileHash -LiteralPath $asar -Algorithm SHA256).Hash
$afterCodexHash = (Get-FileHash -LiteralPath $officialCodex -Algorithm SHA256).Hash
if ($beforeAsarHash -ne $afterAsarHash -or $beforeCodexHash -ne $afterCodexHash) {
    Fail "official package hash changed during installation"
}
$configTemporary = $configPath + ".new"
$config | ConvertTo-Json | Set-Content -LiteralPath $configTemporary -Encoding UTF8
Move-Item -LiteralPath $configTemporary -Destination $configPath -Force
Set-PrivateStateAcl $stateRoot
if ($primaryRequiresAclMigration) {
    Set-PrivateStateAcl $primaryCodexHome
    "Private ACL migration completed by installer v2." | Set-Content -LiteralPath $primaryAclMarker -Encoding UTF8
    Set-PrivateAclEntry -Path $primaryAclMarker -IsDirectory $false
} else {
    # A protected root gives newly-created credentials the private inherited ACL.
    # Rewalking a mature Codex home on every upgrade can otherwise take minutes.
    Set-PrivateAclEntry -Path $primaryCodexHome -IsDirectory $true
}

$stateReceipt = [ordered]@{
    installed = $true
    officialExecutable = $official
    officialCodexExecutable = $officialCodex
    codexBackendExecutable = $codexBackend
    muxExecutable = $muxExecutable
    muxSha256 = $muxHash
    buildId = $buildId
    stateRoot = $stateRoot
    primaryCodexHome = $primaryCodexHome
    officialAppAsarSha256 = $afterAsarHash
    officialCodexSha256 = $afterCodexHash
    codexBackendSha256 = $beforeBackendHash
    existingUserCodexPath = Join-Path $env:USERPROFILE ".codex"
    launcher = Join-Path $installRoot "Codex Subscription Router.cmd"
    dashboardLauncher = Join-Path $installRoot "Open Subscription Router.cmd"
}
Write-Output (ConvertTo-Json -Depth 3 $stateReceipt)

if (-not $NoLaunch) {
    try {
        $startOutput = & (Join-Path $PSHOME "powershell.exe") -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File $startScript -InstallRoot $installRoot 2>&1
        if ($LASTEXITCODE -ne 0) { throw "new router start process exited with code $LASTEXITCODE" }
        $runtimeReceipt = $startOutput | Where-Object { $_ -is [string] -and $_.TrimStart().StartsWith("{") } | Select-Object -Last 1 | ConvertFrom-Json
        if (-not $runtimeReceipt -or [string]$runtimeReceipt.build -ne [string]$config.buildId) {
            throw "new router did not return an exact matching readiness receipt"
        }
    } catch {
        $newFailure = $_.Exception.Message
        if ($env:CODEX_MUX_ACCEPTANCE_TEST -eq "simulate-readiness-failure") {
            Remove-Item Env:CODEX_MUX_ACCEPTANCE_TEST -ErrorAction SilentlyContinue
        }
        $rollbackFailure = $null
        if ($previousConfig) {
            $previousConfig | Set-Content -LiteralPath $configPath -Encoding UTF8
            try {
                $previous = $previousConfig | ConvertFrom-Json
                $previousStart = if (-not [string]::IsNullOrWhiteSpace([string]$previous.startScript)) { [string]$previous.startScript } else { Join-Path $installRoot "start-router.ps1" }
                if ([int]$previous.schemaVersion -ge 2) {
                    $rollbackOutput = & (Join-Path $PSHOME "powershell.exe") -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File $previousStart -InstallRoot $installRoot 2>&1
                    if ($LASTEXITCODE -ne 0) { throw "previous router start process exited with code $LASTEXITCODE" }
                    $restored = $rollbackOutput | Where-Object { $_ -is [string] -and $_.TrimStart().StartsWith("{") } | Select-Object -Last 1 | ConvertFrom-Json
                    if ([string]$restored.build -ne [string]$previous.buildId) { throw "restored daemon build does not match previous configuration" }
                }
            } catch { $rollbackFailure = $_.Exception.Message }
        }
        if ($rollbackFailure) { Fail "new router failed readiness ($newFailure) and rollback failed ($rollbackFailure)" }
        if ($previousConfig) { Fail "new router failed readiness and the previous installed daemon was restored and verified: $newFailure" }
        Fail "new router failed readiness and no previous installation was available: $newFailure"
    }

    foreach ($script in @(
        @{ Source = "scripts\windows\launch-router.ps1"; Destination = "launch-router.ps1" },
        @{ Source = "scripts\windows\launch-router.cmd"; Destination = "Codex Subscription Router.cmd" },
        @{ Source = "scripts\windows\start-router.ps1"; Destination = "start-router.ps1" },
        @{ Source = "scripts\windows\open-dashboard.ps1"; Destination = "open-dashboard.ps1" },
        @{ Source = "scripts\windows\open-dashboard.cmd"; Destination = "Open Subscription Router.cmd" }
    )) {
        $destination = Join-Path $installRoot $script.Destination
        $temporary = $destination + ".new"
        Copy-Item -LiteralPath (Join-Path $sourceRoot $script.Source) -Destination $temporary -Force
        Move-Item -LiteralPath $temporary -Destination $destination -Force
    }

    $taskService = New-Object -ComObject "Schedule.Service"
    $taskService.Connect()
    $taskDefinition = $taskService.NewTask(0)
    $taskDefinition.RegistrationInfo.Description = "Starts the local Codex Subscription Router daemon when this user signs in."
    $taskDefinition.Settings.Enabled = $true
    $taskDefinition.Settings.StartWhenAvailable = $true
    $taskDefinition.Settings.ExecutionTimeLimit = "PT0S"
    $taskDefinition.Settings.MultipleInstances = 2
    $taskDefinition.Settings.DisallowStartIfOnBatteries = $false
    $taskDefinition.Settings.StopIfGoingOnBatteries = $false
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
    $taskDefinition.Principal.UserId = $identity
    $taskDefinition.Principal.LogonType = 3
    $taskDefinition.Principal.RunLevel = 0
    $trigger = $taskDefinition.Triggers.Create(9)
    $trigger.UserId = $identity
    $action = $taskDefinition.Actions.Create(0)
    $action.Path = Join-Path $PSHOME "powershell.exe"
    $action.Arguments = "-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$startScript`" -InstallRoot `"$installRoot`""
    $action.WorkingDirectory = $installRoot
    $taskService.GetFolder("\").RegisterTaskDefinition("Codex Subscription Router", $taskDefinition, 6, $identity, $null, 3, $null) | Out-Null

    $dashboardUrl = & $dashboardScript -InstallRoot $installRoot | Select-Object -Last 1

    $retainedVersions = @((Get-NormalizedPath $versionRoot))
    if ($previousConfigObject -and -not [string]::IsNullOrWhiteSpace([string]$previousConfigObject.muxExecutable)) {
        $previousVersionRoot = Get-NormalizedPath (Split-Path -Parent ([string]$previousConfigObject.muxExecutable))
        if (Test-PathWithin $previousVersionRoot $versionsRoot) { $retainedVersions += $previousVersionRoot }
    }
    Get-ChildItem -LiteralPath $versionsRoot -Directory -ErrorAction SilentlyContinue |
        Where-Object {
            $candidateVersion = Get-NormalizedPath $_.FullName
            (Test-PathWithin $candidateVersion $versionsRoot) -and
                $candidateVersion -notin $retainedVersions -and
                (Test-Path -LiteralPath (Join-Path $candidateVersion "codex-mux.exe") -PathType Leaf)
        } |
        ForEach-Object { Remove-Item -LiteralPath $_.FullName -Recurse -Force }

    Write-Output (ConvertTo-Json -Compress -InputObject ([ordered]@{
        routerPid = [int]$runtimeReceipt.pid
        routerBuild = [string]$runtimeReceipt.build
        controlAddress = [string]$runtimeReceipt.controlAddress
        dashboardUrl = [string]$dashboardUrl
        launchAtSignIn = $true
    }))
}

if ($NoLaunch) {
    foreach ($script in @(
        @{ Source = "scripts\windows\launch-router.ps1"; Destination = "launch-router.ps1" },
        @{ Source = "scripts\windows\launch-router.cmd"; Destination = "Codex Subscription Router.cmd" },
        @{ Source = "scripts\windows\start-router.ps1"; Destination = "start-router.ps1" },
        @{ Source = "scripts\windows\open-dashboard.ps1"; Destination = "open-dashboard.ps1" },
        @{ Source = "scripts\windows\open-dashboard.cmd"; Destination = "Open Subscription Router.cmd" }
    )) {
        Copy-Item -LiteralPath (Join-Path $sourceRoot $script.Source) -Destination (Join-Path $installRoot $script.Destination) -Force
    }
}
