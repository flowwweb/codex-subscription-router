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
        $candidate = Join-Path $package.InstallLocation "ChatGPT.exe"
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            return $candidate
        }
    }

    $windowsApps = Join-Path ${env:ProgramFiles} "WindowsApps"
    $candidates = @()
    if (Test-Path -LiteralPath $windowsApps -PathType Container) {
        $candidates = Get-ChildItem -LiteralPath $windowsApps -Directory -Filter "OpenAI.Codex_*" -ErrorAction SilentlyContinue |
            ForEach-Object { Join-Path $_.FullName "ChatGPT.exe" } |
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
$muxExecutable = Join-Path $installRoot "codex-mux.exe"
$stateRoot = Join-Path $installRoot "state"
$primaryCodexHome = Join-Path $installRoot "primary-codex-home"

New-Item -ItemType Directory -Force -Path $installRoot,$stateRoot,$primaryCodexHome | Out-Null

Push-Location $sourceRoot
try {
    & go.exe build -trimpath -ldflags "-s -w" -o $muxExecutable ./cmd/codex-mux
    if ($LASTEXITCODE -ne 0) {
        Fail "Go multiplexer build failed"
    }
} finally {
    Pop-Location
}

Copy-Item -LiteralPath (Join-Path $sourceRoot "scripts\windows\launch-router.ps1") -Destination (Join-Path $installRoot "launch-router.ps1") -Force
Copy-Item -LiteralPath (Join-Path $sourceRoot "scripts\windows\launch-router.cmd") -Destination (Join-Path $installRoot "Codex Subscription Router.cmd") -Force

$config = [ordered]@{
    schemaVersion = 1
    officialExecutable = $official
    officialCodexExecutable = $officialCodex
    codexBackendExecutable = $codexBackend
    muxExecutable = $muxExecutable
    stateRoot = $stateRoot
    primaryCodexHome = $primaryCodexHome
    officialVersion = (Get-Item -LiteralPath $official).VersionInfo.ProductVersion
    appAsarSha256 = $beforeAsarHash
    officialCodexSha256 = $beforeCodexHash
    codexBackendSha256 = $beforeBackendHash
}
$configPath = Join-Path $installRoot "router-config.json"
$config | ConvertTo-Json | Set-Content -LiteralPath $configPath -Encoding UTF8
Set-PrivateStateAcl $stateRoot
Set-PrivateStateAcl $primaryCodexHome

$afterAsarHash = (Get-FileHash -LiteralPath $asar -Algorithm SHA256).Hash
$afterCodexHash = (Get-FileHash -LiteralPath $officialCodex -Algorithm SHA256).Hash
if ($beforeAsarHash -ne $afterAsarHash -or $beforeCodexHash -ne $afterCodexHash) {
    Fail "official package hash changed during installation"
}

$stateReceipt = [ordered]@{
    installed = $true
    officialExecutable = $official
    officialCodexExecutable = $officialCodex
    codexBackendExecutable = $codexBackend
    muxExecutable = $muxExecutable
    stateRoot = $stateRoot
    primaryCodexHome = $primaryCodexHome
    officialAppAsarSha256 = $afterAsarHash
    officialCodexSha256 = $afterCodexHash
    codexBackendSha256 = $beforeBackendHash
    existingUserCodexPath = Join-Path $env:USERPROFILE ".codex"
    launcher = Join-Path $installRoot "Codex Subscription Router.cmd"
}
Write-Output (ConvertTo-Json -Depth 3 $stateReceipt)

if (-not $NoLaunch) {
    $env:CODEX_MUX_REAL_CODEX = $codexBackend
    $env:CODEX_MUX_HOME = $stateRoot
    $env:CODEX_HOME = $primaryCodexHome
    $env:CODEX_SQLITE_HOME = $primaryCodexHome
    $routerProcess = Start-Process -FilePath $muxExecutable -ArgumentList @("-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled") -WorkingDirectory $installRoot -PassThru -WindowStyle Hidden
    Write-Output (ConvertTo-Json -Compress -InputObject ([ordered]@{
        routerPid = $routerProcess.Id
        routerExecutable = $muxExecutable
        stateRoot = $stateRoot
        primaryCodexHome = $primaryCodexHome
    }))
}
