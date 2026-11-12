//go:build !windows

package store

import (
	"fmt"
	"os"
)

func isUnsupported(error) bool { return false }

// CheckPerm returns an error if a secret file can be read by group or others.
func CheckPerm(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("%s has mode %04o; want 0600 (run: chmod 600 %s)", path, mode, path)
	}
	return nil
}
