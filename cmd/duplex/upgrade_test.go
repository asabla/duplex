package main

import (
	"os"
	"path/filepath"
	"testing"

	"duplex/internal/cache"
	"duplex/internal/catalog"
	"duplex/internal/config"
)

func TestUpgradeCommandPreviewsEmptyInstallationAndChecksOwnership(t *testing.T) {
	dir := t.TempDir()

	file := filepath.Join(dir, "config.yaml")
	if err := config.Create(file, filepath.Join(dir, "state")); err != nil {
		t.Fatal(err)
	}

	manager, err := config.Open(file)
	if err != nil {
		t.Fatal(err)
	}

	c := manager.Snapshot().Config
	if err = os.MkdirAll(c.CacheDir, 0o700); err != nil {
		t.Fatal(err)
	}

	store, err := catalog.Open(filepath.Join(c.DataDir, "catalogue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"upgrade-layouts", "--config", file}, {"upgrade-layouts", "--config", file, "--apply"}} {
		if err = execute(t.Context(), args); err != nil {
			t.Fatal(err)
		}
	}

	release, err := cache.LockMaintenance(c.CacheDir)
	if err != nil {
		t.Fatal(err)
	}

	if err = execute(t.Context(), []string{"upgrade-layouts", "--config", file}); err == nil {
		t.Fatal("maintenance accepted owned cache")
	}

	if err = release(); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"upgrade-layouts", "--unknown"}, {"upgrade-layouts", "argument"}, {"upgrade-layouts", "--config", filepath.Join(dir, "absent")}} {
		if err = execute(t.Context(), args); err == nil {
			t.Fatal("invalid request accepted", args)
		}
	}
}
