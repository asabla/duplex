package config

import (
	"errors"
	"os"
	"path/filepath"
)

// AtomicWrite publishes a synced temporary file and then syncs its parent directory.
func AtomicWrite(path string, b []byte, mode os.FileMode) (result error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".duplex-write-*")
	if err != nil {
		return err
	}

	name := f.Name()
	defer func() {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()

	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}

	if err == nil {
		err = f.Sync()
	}

	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}

	if err = os.Rename(name, path); err != nil {
		return err
	}

	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}

	return errors.Join(directory.Sync(), directory.Close())
}
