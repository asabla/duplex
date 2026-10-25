//go:build linux

package cache

import (
	"context"
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
		defer store.Close()
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
	binary, _ := os.Executable()
	child := exec.Command(binary, "-test.run=^TestInterruptedChunkPublication$")
	child.Env = append(os.Environ(), "DUPLEX_CACHE_CHILD="+dir)
	if e := child.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		files, _ := filepath.Glob(filepath.Join(dir, ".part-*"))
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
	_ = child.Wait()
	store, e := New(dir, 128<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if store.Stats().Chunks != 0 {
		t.Fatal("interrupted data published")
	}
	files, _ := filepath.Glob(filepath.Join(dir, ".part-*"))
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
