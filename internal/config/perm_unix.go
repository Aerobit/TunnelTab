//go:build !windows

package config

import "os"

// RestrictToOwner makes the data folder accessible to the current user only
// (mode 0700). Files inside are created 0600.
func RestrictToOwner(dir string) error {
	return os.Chmod(dir, 0o700)
}
