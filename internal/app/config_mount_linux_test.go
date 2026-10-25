//go:build linux

package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
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

func TestConfigurationOpensBeforeDisconnectedMountRecovery(t *testing.T) {
	if os.Getenv("DUPLEX_FUSE_TEST") != "1" {
		t.Skip("requires explicit Linux FUSE qualification")
	}

	if file := os.Getenv("DUPLEX_CONFIG_MOUNT_CHILD"); file != "" {
		manager, err := config.Open(file)
		if err != nil {
			t.Fatal(err)
		}

		mount, err := library.MountProvider(manager.Snapshot().Config.Mountpoint, catalogueFS{app: &App{Config: manager}})
		if err != nil {
			t.Fatal(err)
		}
		defer closeTest(t, mount)

		if err = os.WriteFile(file+".ready", []byte("ready"), 0o600); err != nil { //nolint:gosec // G703: The parent supplies this readiness path beneath its temporary crash-fixture directory.
			t.Fatal(err)
		}

		select {}
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

	mountpoint := manager.Snapshot().Config.Mountpoint
	defer func() {
		if failure := syscall.Unmount(mountpoint, syscall.MNT_DETACH); failure != nil && !errors.Is(failure, syscall.EINVAL) && !errors.Is(failure, syscall.ENOENT) {
			t.Error(failure)
		}
	}()

	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	child := exec.CommandContext(t.Context(), binary, "-test.run=^TestConfigurationOpensBeforeDisconnectedMountRecovery$") //nolint:gosec // G204: The fixed child command runs this test executable only.

	child.Env = append(os.Environ(), "DUPLEX_CONFIG_MOUNT_CHILD="+file)
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if child.ProcessState != nil {
			return
		}

		if failure := child.Process.Kill(); failure != nil && !errors.Is(failure, os.ErrProcessDone) {
			t.Error(failure)
		}

		var exit *exec.ExitError
		if failure := child.Wait(); failure != nil && !errors.As(failure, &exit) {
			t.Error(failure)
		}
	}()

	deadline := time.Now().Add(10 * time.Second)

	for {
		if _, err = os.Stat(file + ".ready"); err == nil {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("child mount did not start")
		}

		time.Sleep(10 * time.Millisecond)
	}

	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	var exit *exec.ExitError
	if err = child.Wait(); !errors.As(err, &exit) {
		t.Fatal("crash fixture did not exit by signal", err)
	}

	recovered, err := config.Open(file)
	if err != nil {
		t.Fatal("disconnected mount prevented configuration load", err)
	}

	alias := filepath.Join(root, "alias")
	if err = os.Symlink(mountpoint, alias); err != nil {
		t.Fatal(err)
	}

	snapshot := recovered.Snapshot()

	snapshot.Config.Mountpoint = alias
	if err = recovered.Save(snapshot.Config, snapshot.Revision); err == nil {
		t.Fatal("alias to disconnected mount accepted")
	}

	mount, err := library.MountProvider(mountpoint, catalogueFS{app: &App{Config: recovered}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, mount)

	if !mount.Ready() {
		t.Fatal("mount did not recover")
	}

	if _, err = os.Stat(mountpoint); err != nil {
		t.Fatal(err)
	}
}
