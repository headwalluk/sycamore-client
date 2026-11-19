//go:build !windows

package tunnel

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// privateDirName is the per-user fallback directory's name.
func privateDirName() string {
	return "sycamore-" + strconv.Itoa(os.Getuid())
}

// checkPrivate accepts a real directory (not a symlink) owned by this user
// with no group or other permissions.
func checkPrivate(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("not a directory")
	}
	stat, known := info.Sys().(*syscall.Stat_t)
	if !known {
		return errors.New("can't read its owner")
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("owned by uid %d, not %d", stat.Uid, os.Getuid())
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("mode %#o is open to other users", info.Mode().Perm())
	}
	return nil
}
