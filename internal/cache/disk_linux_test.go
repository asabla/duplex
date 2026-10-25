//go:build linux

package cache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPhysicalDiskExhaustion(t *testing.T) {
	root := os.Getenv("DUPLEX_DISK_FULL_DIR")
	if root == "" {
		t.Skip("requires a dedicated small Linux tmpfs")
	}
	store, e := New(filepath.Join(root, "chunks"), 50<<30)
	if e != nil {
		t.Fatal(e)
	}
	defer closeTest(t, store)
	// Force a reservation/write race: another process can fill the disk after a
	// successful capacity probe. The writer must still fail without publication.
	store.space = func() (int64, int64, error) { return 1 << 50, 1 << 50, nil }
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i)
	}
	failed := false
	for i := range 8 {
		b, release, e := store.Get(context.Background(), "disk-full", int64(i<<20), len(data), func(context.Context) ([]byte, error) { return data, nil })
		if e != nil {
			failed = true
			break
		}
		if len(b) != len(data) {
			t.Fatal("short ready chunk")
		}
		release()
	}
	if !failed {
		t.Fatal("test filesystem did not run out of space")
	}
	b, release, e := store.Get(context.Background(), "disk-full", 0, len(data), func(context.Context) ([]byte, error) { t.Fatal("published chunk lost"); return nil, nil })
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	for i := range b {
		if b[i] != data[i] {
			t.Fatal("published data corrupted after disk-full")
		}
	}
	files, readErr := os.ReadDir(filepath.Join(root, "chunks"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, file := range files {
		if len(file.Name()) > 6 && file.Name()[:6] == ".part-" {
			t.Fatal("unfinished write left after disk-full")
		}
	}
}

func TestPhysicalPressureEvictsOnlyUnpinnedChunks(t *testing.T) {
	root := os.Getenv("DUPLEX_PRESSURE_DIR")
	if root == "" {
		t.Skip("requires a dedicated small Linux tmpfs")
	}
	store, e := New(filepath.Join(root, "cache"), 50<<30)
	if e != nil {
		t.Fatal(e)
	}
	defer closeTest(t, store)
	data := make([]byte, 1<<20)
	data[0] = 19
	for n := range 8 {
		b, release, e := store.Get(context.Background(), "pressure", int64(n<<20), len(data), func(context.Context) ([]byte, error) { return data, nil })
		if e != nil {
			t.Fatal(e)
		}
		if b[0] != 19 {
			t.Fatal("incorrect data")
		}
		release()
	}
	if store.Stats().Allocated > 1<<20 {
		t.Fatal("cold chunks did not yield to physical pressure")
	}
	b, release, e := store.Get(context.Background(), "pressure", 7<<20, len(data), func(context.Context) ([]byte, error) { t.Fatal("last ready chunk lost"); return nil, nil })
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if b[0] != 19 {
		t.Fatal("incorrect pinned data")
	}
	if _, _, e = store.Get(context.Background(), "pressure", 8<<20, len(data), func(context.Context) ([]byte, error) { t.Fatal("fetch started without space"); return nil, nil }); !errors.Is(e, ErrFull) {
		t.Fatal("physical pressure evicted a pinned chunk", e)
	}
}
