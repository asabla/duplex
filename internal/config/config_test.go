package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalEditsAndStaleSave(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if e := Create(file, filepath.Join(dir, "state")); e != nil {
		t.Fatal(e)
	}
	m, e := Open(file)
	if e != nil {
		t.Fatal(e)
	}
	original := m.Snapshot()
	if e = os.WriteFile(file, []byte("version: [invalid"), 0o600); e != nil {
		t.Fatal(e)
	}
	if m.Reload() {
		t.Fatal("invalid edit loaded")
	}
	if m.Snapshot().Config.CacheGiB != 50 || m.Snapshot().Error == "" {
		t.Fatal("last valid settings lost")
	}
	if e = m.Save(original.Config, original.Revision); !errors.Is(e, ErrConflict) {
		t.Fatal("stale save overwrote invalid external edit", e)
	}
	if e = AtomicWrite(file, []byte("version: 1\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	if m.Reload() {
		t.Fatal("incomplete edit accepted")
	}
	c := original.Config
	c.CacheGiB = 12
	b, e := os.ReadFile(filepath.Join(dir, "config.yaml")) //nolint:gosec // G304: This reads only a generated configuration in t.TempDir.
	if e != nil {
		t.Fatal(e)
	}
	_ = b
	// Restore a complete valid external file through a separate manager instance.
	other := filepath.Join(dir, "other.yaml")
	if e = Create(other, filepath.Join(dir, "state")); e != nil {
		t.Fatal(e)
	}
	n, err := Open(other)
	if err != nil {
		t.Fatal(err)
	}
	if e = n.Save(c, n.Snapshot().Revision); e != nil {
		t.Fatal(e)
	}
	data, err := os.ReadFile(other) //nolint:gosec // G304: This reads an alternate generated configuration in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if e = AtomicWrite(file, data, 0o600); e != nil {
		t.Fatal(e)
	}
	if !m.Reload() || m.Snapshot().Config.CacheGiB != 12 || m.Snapshot().Error != "" {
		t.Fatal("valid external edit did not load")
	}
}

func TestRejectUnsafeChannelAndOverlappingStorage(t *testing.T) {
	for _, u := range []string{"http://www.youtube.com/@a", "https://youtube.com.evil.test/@a", "https://user:secret@youtube.com/@a", "https://youtube.com/watch?v=x", "https://youtube.com/@a?x=1", "https://youtube.com/@a/other"} {
		if _, e := ChannelURL(u); e == nil {
			t.Fatal("accepted", u)
		}
	}
	c := Default(filepath.Join(t.TempDir(), "state"))
	c.CacheDir = filepath.Join(c.DataDir, "cache")
	if c.Validate() == nil {
		t.Fatal("overlapping cache/catalogue accepted")
	}
}

func TestStorageAliasesCannotOverlapDurableAndEvictableData(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(state, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(filepath.Join(state, "data"), alias); err != nil {
		t.Fatal(err)
	}
	config := Default(state)
	config.CacheDir = filepath.Join(alias, "chunks")
	if err := config.Validate(); err == nil {
		t.Fatal("cache alias overlaps durable data")
	}
	config = Default(state)
	config.Mountpoint = filepath.Join(alias, "library")
	if err := config.Validate(); err == nil {
		t.Fatal("mount alias overlaps durable data")
	}
	config = Default(state)
	config.CacheDir = alias + "/../ambiguous"
	if err := config.Validate(); err == nil {
		t.Fatal("unclean alias path accepted")
	}
	config = Default(state)
	config.CacheDir = filepath.Join(root, "separate", "not-created", "chunks")
	if err := config.Validate(); err != nil {
		t.Fatal("dedicated future directory was rejected", err)
	}
}

func TestConcurrentSavesKeepRevisionConflict(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := Create(file, filepath.Join(t.TempDir(), "state")); err != nil {
		t.Fatal(err)
	}
	manager, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	original := manager.Snapshot()
	results := make(chan error, 2)
	for _, limit := range []int{12, 13} {
		go func() {
			settings := original.Config
			settings.CacheGiB = limit
			results <- manager.Save(settings, original.Revision)
		}()
	}
	successes, conflicts := 0, 0
	for range 2 {
		switch failure := <-results; {
		case failure == nil:
			successes++
		case errors.Is(failure, ErrConflict):
			conflicts++
		default:
			t.Fatal(failure)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("concurrent stale save accepted", successes, conflicts)
	}
	if manager.Reload() {
		t.Fatal("saved file and snapshot disagree")
	}
	settings := manager.Snapshot()
	if settings.Config.CacheGiB != 12 && settings.Config.CacheGiB != 13 {
		t.Fatal(settings.Config.CacheGiB)
	}
}
