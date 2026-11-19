//go:build windows

package tunnel

import (
	"errors"
	"os"
)

// privateDirName is the fallback directory's name. The Windows temporary
// directory is already per-user.
func privateDirName() string {
	return "sycamore"
}

// checkPrivate accepts a real directory (not a symlink or junction).
func checkPrivate(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("not a directory")
	}
	return nil
}
