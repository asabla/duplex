package cache

import (
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
)

func (s *Store) recoverDirectory() error {
	files, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.Name() == ".lock" {
			continue
		}

		if err = s.recoverEntry(file); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) recoverEntry(file os.DirEntry) error {
	if file.IsDir() {
		return errors.New("unexpected directory in media cache")
	}

	path := filepath.Join(s.dir, file.Name())
	if strings.HasPrefix(file.Name(), ".part-") {
		return os.Remove(path)
	}

	key, digest, err := chunkName(file.Name())
	if err != nil {
		return err
	}

	info, err := file.Info()
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return errors.New("non-regular cache entry")
	}

	if s.items[key] != nil {
		return errors.New("duplicate cache identity")
	}

	allocation := s.allocation(info)
	if allocation > math.MaxInt64-s.used {
		return errors.New("cache allocation exceeds supported capacity")
	}

	s.items[key] = &entry{path: path, digest: digest, allocation: allocation, used: info.ModTime()}
	s.used += allocation

	return nil
}

func chunkName(name string) (key, digest string, err error) {
	parts := strings.Split(strings.TrimSuffix(name, ".chunk"), "-")
	if !strings.HasSuffix(name, ".chunk") || len(parts) != 2 || len(parts[0]) != 64 || len(parts[1]) != 64 {
		return "", "", errors.New("unexpected file in media cache")
	}

	if _, err = hex.DecodeString(parts[0] + parts[1]); err != nil {
		return "", "", err
	}

	return parts[0], parts[1], nil
}
