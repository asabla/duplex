package app

import (
	"path/filepath"
	"testing"

	"duplex/internal/config"
)

func TestNFSApplicationLifecycleAndFailedListenCleanup(t *testing.T) {
	root := t.TempDir()

	filename := filepath.Join(root, "config.yaml")
	if err := config.Create(filename, filepath.Join(root, "state")); err != nil {
		t.Fatal(err)
	}

	manager, err := config.Open(filename)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = OpenWithNFS(t.Context(), manager, fixtureCollection(0), "invalid address"); err == nil {
		t.Fatal("invalid listener succeeded")
	}

	a, err := OpenWithNFS(t.Context(), manager, fixtureCollection(0), "127.0.0.1:0")
	if err != nil {
		t.Fatal("failed startup retained storage ownership", err)
	}

	if a.mount == nil || !a.mount.Ready() || a.MountDone() == nil {
		t.Fatal("NFS readiness is missing")
	}

	done := a.MountDone()
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	default:
		t.Fatal("NFS listener survived shutdown")
	}
}
