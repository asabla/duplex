//go:build linux || darwin

package cache

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func lockDirectory(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: The operator selects the cache directory; the lock file name is fixed.
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(errors.New("media cache is owned by another process"), f.Close())
	}
	return f, nil
}

func unlockDirectory(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		return err
	}
	return f.Close()
}
