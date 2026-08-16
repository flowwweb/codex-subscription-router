//go:build windows

package state

import (
	"fmt"
	"os/exec"
	"strings"
)

// Windows does not translate Unix mode bits into a useful owner-only ACL for
// files created under an inheritance-disabled router directory. Apply the
// current Windows identity explicitly so state survives atomic replacement
// and can be reopened on the next launch.
func SecureDirectory(path string) error {
	return applyACL(path, "(OI)(CI)F")
}

func SecureFile(path string) error {
	return applyACL(path, "F")
}

func applyACL(path, rights string) error {
	identityOutput, err := exec.Command("whoami.exe").Output()
	if err != nil {
		return fmt.Errorf("resolve Windows identity: %w", err)
	}
	identity := strings.TrimSpace(string(identityOutput))
	if identity == "" {
		return fmt.Errorf("resolve Windows identity: empty whoami result")
	}
	command := exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", identity+":"+rights)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("apply Windows ACL to %s: %w (%s)", path, err, strings.TrimSpace(string(output)))
	}
	return nil
}
