package app

import (
	"path/filepath"
	"testing"

	"duplex/internal/config"
)

func TestSecondServiceOpenDoesNotInterruptLiveDiscovery(t *testing.T) {
	root := t.TempDir()

	file := filepath.Join(root, "config.yaml")
	if err := config.Create(file, filepath.Join(root, "state")); err != nil {
		t.Fatal(err)
	}

	manager, err := config.Open(file)
	if err != nil {
		t.Fatal(err)
	}

	application, err := Open(t.Context(), manager, fixtureCollection(0), true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, application)

	job, err := application.Catalog.StartJob(t.Context(), testOwner)
	if err != nil {
		t.Fatal(err)
	}

	second, err := Open(t.Context(), manager, fixtureCollection(0), true)
	if err == nil {
		closeTest(t, second)
		t.Fatal("second service acquired the live media cache")
	}

	var state string
	if err = application.Catalog.DB.QueryRowContext(t.Context(), `SELECT state FROM sync_jobs WHERE id=?`, job).Scan(&state); err != nil || state != "running" {
		t.Fatal("failed second start altered the active job", state, err)
	}
}
