//go:build linux

package cache

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestInterruptedChunkPublication(t *testing.T) {
	if os.Getenv("DUPLEX_CACHE_CRASH_TEST") != "1" {
		t.Skip("requires explicit isolated Linux crash test")
	}
	if dir := os.Getenv("DUPLEX_CACHE_CHILD"); dir != "" {
		store, e := New(dir, 128<<20)
		if e != nil {
			t.Fatal(e)
		}
		defer closeTest(t, store)
		data := make([]byte, 64<<20)
		for i := range data {
			data[i] = byte(i)
		}
		_, release, e := store.Get(context.Background(), "interrupted", 0, len(data), func(context.Context) ([]byte, error) { return data, nil })
		if e != nil {
			t.Fatal(e)
		}
		release()
		select {}
	}
	dir := filepath.Join(t.TempDir(), "cache")
	binary, executableErr := os.Executable()
	if executableErr != nil {
		t.Fatal(executableErr)
	}
	child := exec.CommandContext(t.Context(), binary, "-test.run=^TestInterruptedChunkPublication$") //nolint:gosec // G204: The child is this test executable with fixed crash-fixture arguments.
	child.Env = append(os.Environ(), "DUPLEX_CACHE_CHILD="+dir)
	if e := child.Start(); e != nil {
		t.Fatal(e)
	}
	defer stopCrashChild(t, child)
	deadline := time.Now().Add(10 * time.Second)
	for {
		files, globErr := filepath.Glob(filepath.Join(dir, ".part-*"))
		if globErr != nil {
			t.Fatal(globErr)
		}
		if len(files) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unfinished write not observed")
		}
		time.Sleep(time.Millisecond)
	}
	if e := child.Process.Kill(); e != nil {
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
	store, e := New(dir, 128<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer closeTest(t, store)
	if store.Stats().Chunks != 0 {
		t.Fatal("interrupted data published")
	}
	files, globErr := filepath.Glob(filepath.Join(dir, ".part-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(files) != 0 {
		t.Fatal("unfinished writes not recovered")
	}
	data, release, e := store.Get(context.Background(), "after-restart", 0, 4096, func(context.Context) ([]byte, error) { b := make([]byte, 4096); b[0] = 37; return b, nil })
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if data[0] != 37 {
		t.Fatal("incorrect recovered data")
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
