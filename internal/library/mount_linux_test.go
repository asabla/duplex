//go:build linux

package library

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestFuseSnapshotAndOpenHandle(t *testing.T) {
	if os.Getenv("DUPLEX_FUSE_TEST") != "1" {
		t.Skip("requires explicit Linux FUSE test container")
	}

	dir := t.TempDir()

	m, e := MountReadOnly(dir, []Entry{{Path: "Channel/episode.mp4", Media: blob{id: "old", data: []byte("old bytes")}}})
	if e != nil {
		t.Fatal(e)
	}
	defer closeTest(t, m)

	if !m.Ready() {
		t.Fatal("mounted filesystem not ready")
	}

	old, e := os.Open(filepath.Join(dir, "Channel/episode.mp4")) //nolint:gosec // G304: This path is an isolated FUSE fixture beneath t.TempDir.
	if e != nil {
		t.Fatal(e)
	}
	defer closeTest(t, old)

	if e = m.Update([]Entry{{Path: "Channel/episode.mp4", Media: blob{id: "new", data: []byte("new representation")}}}); e != nil {
		t.Fatal(e)
	}

	data, e := os.ReadFile(filepath.Join(dir, "Channel/episode.mp4")) //nolint:gosec // G304: This path is an isolated FUSE fixture beneath t.TempDir.
	if e != nil || string(data) != "new representation" {
		t.Fatal(string(data), e)
	}

	b := make([]byte, 9)
	if _, e = old.ReadAt(b, 0); e != nil || string(b) != "old bytes" {
		t.Fatal("open handle changed revision", string(b), e)
	}

	if _, e = os.OpenFile(filepath.Join(dir, "Channel/episode.mp4"), os.O_WRONLY, 0); e == nil { //nolint:gosec // G304: This path is an isolated FUSE fixture beneath t.TempDir.
		t.Fatal("writable media")
	}

	if e = m.Update(nil); e != nil {
		t.Fatal(e)
	}

	if _, e = old.ReadAt(b, 0); e != nil || string(b) != "old bytes" {
		t.Fatal("unlinked active handle failed", e)
	}
}

func TestFuseCrashRecovery(t *testing.T) {
	if os.Getenv("DUPLEX_FUSE_TEST") != "1" {
		t.Skip("requires explicit Linux FUSE test container")
	}

	if dir := os.Getenv("DUPLEX_CRASH_CHILD"); dir != "" {
		m, e := MountReadOnly(dir, []Entry{{Path: "episode.mp4", Media: blob{id: "before", data: []byte("before crash")}}})
		if e != nil {
			t.Fatal(e)
		}
		defer closeTest(t, m)

		if e = os.WriteFile(dir+".ready", []byte("ready"), 0o600); e != nil { //nolint:gosec // G703: The child writes its readiness marker under the parent test directory.
			t.Fatal(e)
		}

		select {}
	}

	root := t.TempDir()
	dir := filepath.Join(root, "library")

	binary, executableErr := os.Executable()
	if executableErr != nil {
		t.Fatal(executableErr)
	}

	child := exec.CommandContext(t.Context(), binary, "-test.run=^TestFuseCrashRecovery$") //nolint:gosec // G204: The child is this test executable with fixed crash-fixture arguments.

	child.Env = append(os.Environ(), "DUPLEX_CRASH_CHILD="+dir)
	if e := child.Start(); e != nil {
		t.Fatal(e)
	}

	defer stopCrashChild(t, child)

	deadline := time.Now().Add(10 * time.Second)

	for {
		if _, e := os.Stat(dir + ".ready"); e == nil {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("child mount not ready")
		}

		time.Sleep(10 * time.Millisecond)
	}

	old, e := os.Open(filepath.Join(dir, "episode.mp4")) //nolint:gosec // G304: This path is an isolated FUSE fixture beneath t.TempDir.
	if e != nil {
		t.Fatal(e)
	}

	defer func() {
		if err := old.Close(); err != nil && !errors.Is(err, syscall.ENOTCONN) {
			t.Error(err)
		}
	}()

	if e = child.Process.Kill(); e != nil {
		t.Fatal(e)
	}

	if err := child.Wait(); err == nil {
		t.Fatal("killed fixture exited successfully")
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
	}

	next, e := MountReadOnly(dir, []Entry{{Path: "episode.mp4", Media: blob{id: "after", data: []byte("after restart")}}})
	if e != nil {
		t.Fatal(e)
	}
	defer closeTest(t, next)

	b := make([]byte, 12)
	if _, e = old.ReadAt(b, 0); e == nil {
		t.Fatal("stale handle returned success")
	}

	data, e := os.ReadFile(filepath.Join(dir, "episode.mp4")) //nolint:gosec // G304: This path is an isolated FUSE fixture beneath t.TempDir.
	if e != nil || string(data) != "after restart" {
		t.Fatal(string(data), e)
	}

	if _, e = MountReadOnly(dir, nil); e == nil {
		t.Fatal("live mount was replaced")
	}
}

func stopCrashChild(t *testing.T, child *exec.Cmd) {
	t.Helper()

	if child.ProcessState != nil {
		return
	}

	if err := child.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Error(err)
	}

	if err := child.Wait(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Error(err)
		}
	}
}

func closeTest(t *testing.T, closer io.Closer) {
	t.Helper()

	if err := closer.Close(); err != nil {
		t.Error(err)
	}
}
