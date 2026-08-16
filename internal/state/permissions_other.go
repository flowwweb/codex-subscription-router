//go:build !windows

package state

// SecureDirectory and SecureFile are no-ops on Unix because the existing
// os.Chmod calls provide the owner-only permissions used by the router.
func SecureDirectory(path string) error { return nil }

func SecureFile(path string) error { return nil }
