package config

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
)

func validateStorage(paths []string) error {
	resolved := make([]string, 0, len(paths))
	for index, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return errors.New("storage and mount paths must be absolute dedicated directories")
		}
		canonical, err := resolveStoragePath(path, index == len(paths)-1)
		if err != nil {
			return err
		}
		if canonical == "/" {
			return errors.New("storage and mount paths must be dedicated directories")
		}
		for _, previous := range resolved {
			relative, err := filepath.Rel(previous, canonical)
			if err != nil {
				return err
			}
			reverse, err := filepath.Rel(canonical, previous)
			if err != nil {
				return err
			}
			if !outside(relative) || !outside(reverse) {
				return errors.New("storage and mount directories must be separate")
			}
		}
		resolved = append(resolved, canonical)
	}
	return nil
}

func resolveStoragePath(path string, mountpoint bool) (string, error) {
	canonical, err := resolveExistingPath(path)
	if !mountpoint || !errors.Is(err, syscall.ENOTCONN) {
		return canonical, err
	}
	// A disconnected mount must reach the mount layer, which checks ownership
	// before detaching it. Do not relax alias checks for symlinks or parents.
	if _, leafErr := os.Lstat(path); !errors.Is(leafErr, syscall.ENOTCONN) {
		return "", err
	}
	parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
	if parentErr != nil {
		return "", parentErr
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func resolveExistingPath(path string) (string, error) {
	suffix := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		suffix = append(suffix, filepath.Base(path))
		path = parent
	}
}

func validatePlex(p Plex) error {
	if p.URL == "" {
		return nil
	}
	u, err := url.Parse(p.URL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("connection to Plex must use an HTTP(S) origin")
	}
	return nil
}

func validateChannels(channels []Channel) error {
	seen := map[string]bool{}
	for _, ch := range channels {
		if !validID(ch.ID) || seen[ch.ID] {
			return errors.New("invalid or duplicate channel settings")
		}
		if err := ch.validatePolicy(); err != nil {
			return err
		}
		seen[ch.ID] = true
		for _, id := range ch.Unlisted {
			if !validVideoID(id) {
				return errors.New("unlisted video IDs must contain 11 URL-safe characters")
			}
		}
		if _, err := ChannelURL(ch.URL); err != nil {
			return err
		}
	}
	return nil
}

func validVideoID(id string) bool {
	if len(id) != 11 {
		return false
	}
	for _, r := range id {
		if !urlCharacter(r) {
			return false
		}
	}
	return true
}

func urlCharacter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
}

func outside(p string) bool {
	return p == ".." || len(p) > 3 && p[:3] == ".."+string(filepath.Separator)
}

func validID(s string) bool {
	if len(s) != 24 || s[:2] != "UC" {
		return false
	}
	for _, r := range s {
		valid := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !valid {
			return false
		}
	}
	return true
}
