//go:build windows

package store

import (
	"errors"
	"os"
)

// Chmod on Windows only toggles the read-only bit; treat failures as harmless.
func isUnsupported(err error) bool { return errors.Is(err, os.ErrPermission) }

// CheckPerm is a no-op on Windows, where Unix modes don't apply. Protect the
// store with folder ACLs instead.
func CheckPerm(path string) error {
	_, err := os.Stat(path)
	return err
}
