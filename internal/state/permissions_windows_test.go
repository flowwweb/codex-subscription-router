//go:build windows

package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSecureDirectoryRemovesUnexpectedExplicitPrincipal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if output, err := exec.Command("icacls.exe", path, "/grant", "*S-1-1-0:(OI)(CI)F").CombinedOutput(); err != nil {
		t.Fatalf("seed unexpected ACL: %v (%s)", err, output)
	}
	if err := SecureDirectory(path); err != nil {
		t.Fatal(err)
	}

	const verifyScript = `$ErrorActionPreference = 'Stop'
$item = New-Object System.IO.DirectoryInfo($env:CODEX_MUX_ACL_TEST_TARGET)
$acl = $item.GetAccessControl()
$current = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$allowed = @($current, 'S-1-5-18')
$unexpected = @($acl.Access | Where-Object {
    $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value -notin $allowed
})
if (-not $acl.AreAccessRulesProtected -or $unexpected.Count -gt 0) {
    throw 'unexpected explicit principal survived ACL replacement'
}`
	command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", verifyScript)
	command.Env = append(os.Environ(), "CODEX_MUX_ACL_TEST_TARGET="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("verify private ACL: %v (%s)", err, output)
	}
}
