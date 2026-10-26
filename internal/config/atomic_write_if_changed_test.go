package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestAtomicWriteIfChangedRetainsOnlyIdenticalPrivateFiles(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("index"), bytes.Repeat([]byte("large index"), 10000)} {
		t.Run(strconv.Itoa(len(data)), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "index.json")
			if err := AtomicWriteIfChanged(path, data, 0o600); err != nil {
				t.Fatal(err)
			}

			stamp := time.Unix(1700000000, 0)
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}

			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}

			if err := AtomicWriteIfChanged(path, data, 0o600); err != nil {
				t.Fatal(err)
			}

			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) || !after.ModTime().Equal(stamp) {
				t.Fatal("identical file was replaced", err)
			}

			if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: Deliberately unsafe fixture permissions must be repaired by publication.
				t.Fatal(err)
			}

			if err := AtomicWriteIfChanged(path, data, 0o600); err != nil {
				t.Fatal(err)
			}

			private, err := os.Stat(path)
			if err != nil || private.Mode() != 0o600 || os.SameFile(after, private) {
				t.Fatal("unsafe permissions retained", err)
			}

			changed := append([]byte("different content"), data...)
			if err := AtomicWriteIfChanged(path, changed, 0o600); err != nil {
				t.Fatal(err)
			}

			stored, err := os.ReadFile(path) //nolint:gosec // G304: The file is an explicit fixture beneath t.TempDir.
			if err != nil || !bytes.Equal(stored, changed) {
				t.Fatal("changed content was not published", err)
			}
		})
	}
}

func TestAtomicWriteIfChangedRepairsSameSizeCorruptionAndSymlinks(t *testing.T) {
	root := t.TempDir()

	path, target := filepath.Join(root, "index.json"), filepath.Join(root, "target")
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := AtomicWriteIfChanged(path, []byte("intact"), 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // G304: The file is an explicit fixture beneath t.TempDir.
	if err != nil || string(data) != "intact" {
		t.Fatal("same-size corruption survived", err)
	}

	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	if err := AtomicWriteIfChanged(path, []byte("intact"), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("symlink retained", err)
	}

	if err := AtomicWriteIfChanged(path, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}

	data, err = os.ReadFile(target) //nolint:gosec // G304: The symlink target is an explicit fixture beneath t.TempDir.
	if err != nil || string(data) != "intact" {
		t.Fatal("symlink target changed", err)
	}

	if err := AtomicWriteIfChanged(filepath.Join(root, "missing", "index"), nil, 0o600); err == nil {
		t.Fatal("publication error suppressed")
	}
}
