//go:build windows

package state

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// Windows does not translate Unix mode bits into a useful owner-only ACL for
// files created under an inheritance-disabled router directory. Apply the
// current Windows identity explicitly so state survives atomic replacement
// and can be reopened on the next launch.
func SecureDirectory(path string) error {
	return applyACL(path, true)
}

func SecureFile(path string) error {
	return applyACL(path, false)
}

func applyACL(path string, directory bool) error {
	// icacls /grant:r replaces grants for one identity but deliberately leaves
	// unrelated explicit entries in place. Build and verify the protected DACL
	// instead so a reused state directory cannot retain access for another user.
	const script = `$ErrorActionPreference = 'Stop'
$target = $env:CODEX_MUX_ACL_TARGET
$directory = [bool]::Parse($env:CODEX_MUX_ACL_DIRECTORY)
$current = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
$system = New-Object System.Security.Principal.SecurityIdentifier('S-1-5-18')
$item = if ($directory) {
    New-Object System.IO.DirectoryInfo($target)
} else {
    New-Object System.IO.FileInfo($target)
}
$acl = $item.GetAccessControl()
$acl.SetAccessRuleProtection($true, $false)
foreach ($identity in @($acl.Access | ForEach-Object IdentityReference | Select-Object -Unique)) {
    $acl.PurgeAccessRules($identity)
}
$inheritance = [System.Security.AccessControl.InheritanceFlags]::None
if ($directory) {
    $inheritance = [System.Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
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
    throw 'effective DACL verification failed'
}`
	command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	command.Env = append(os.Environ(),
		"CODEX_MUX_ACL_TARGET="+path,
		"CODEX_MUX_ACL_DIRECTORY="+strconv.FormatBool(directory),
	)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("replace Windows ACL for %s: %w (%s)", path, err, string(output))
	}
	return nil
}
