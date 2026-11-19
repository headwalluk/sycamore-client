package tunnel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// socketDirectory returns where tunnel sockets go: dir when one is given
// ($XDG_RUNTIME_DIR), else a per-user directory under the system temporary
// directory, created mode 0700 and refused unless it's private to this user.
func socketDirectory(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	private := filepath.Join(os.TempDir(), privateDirName())
	if err := os.Mkdir(private, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("tunnel: create socket directory: %w", err)
	}
	if err := checkPrivate(private); err != nil {
		return "", fmt.Errorf("tunnel: socket directory %s: %w", private, err)
	}
	return private, nil
}
