//go:build linux

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"duplex/internal/config"
	"duplex/internal/library"
)

func TestSettingsSaveAndReloadThroughActiveMount(t *testing.T) {
	if os.Getenv("DUPLEX_FUSE_TEST") != "1" {
		t.Skip("requires explicit Linux FUSE qualification")
	}
	root := t.TempDir()
	file := filepath.Join(root, "config.yaml")
	if err := config.Create(file, filepath.Join(root, "state")); err != nil {
		t.Fatal(err)
	}
	manager, err := config.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Config: manager}
	mount, err := library.MountProvider(manager.Snapshot().Config.Mountpoint, catalogueFS{app: a})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			closeTest(t, mount)
		}
	}()
	done := make(chan error, 1)
	go func() {
		snapshot := manager.Snapshot()
		snapshot.Config.CacheGiB = 12
		if failure := manager.Save(snapshot.Config, snapshot.Revision); failure != nil {
			done <- failure
			return
		}
		snapshot.Config.CacheGiB = 13
		data, failure := yaml.Marshal(snapshot.Config)
		if failure == nil {
			failure = config.AtomicWrite(file, data, 0o600)
		}
		if failure == nil && !manager.Reload() {
			failure = os.ErrInvalid
		}
		done <- failure
	}()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		// Unmount releases a blocked validation syscall so test cleanup can finish.
		if err = mount.Close(); err != nil {
			t.Error(err)
		}
		closed = true
		if failure := <-done; failure != nil {
			t.Log("validation after unmount:", failure)
		}
		t.Fatal("configuration validation blocked its own filesystem callback")
	}
	if manager.Snapshot().Config.CacheGiB != 13 {
		t.Fatal("external settings were not applied")
	}
}
