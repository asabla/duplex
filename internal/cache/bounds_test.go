package cache

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestInvalidAndOversizedRequestsDoNotEvictOrFetch(t *testing.T) {
	store, err := New(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, store)
	_, release, err := store.Get(t.Context(), "first", 0, 4, func(context.Context) ([]byte, error) { return []byte("data"), nil })
	if err != nil {
		t.Fatal(err)
	}
	release()
	called := false
	fetch := func(context.Context) ([]byte, error) { called = true; return nil, errors.New("must not fetch") }
	for _, bounds := range []struct {
		offset int64
		length int
	}{{0, 0}, {-1, 4}, {math.MaxInt64, 1}, {0, int(^uint(0) >> 1)}, {0, int(^uint(0)>>1) - 8192}} {
		if _, _, err = store.Get(t.Context(), "bad", bounds.offset, bounds.length, fetch); err == nil {
			t.Fatal("invalid range accepted")
		}
	}
	if _, _, err = store.Get(t.Context(), "oversized", 0, 8192, fetch); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
	if called || store.Stats().Chunks != 1 {
		t.Fatal("invalid request fetched or evicted data")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err = store.Get(ctx, "first", 0, 4, fetch); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCorruptionWithAnotherPinnedReaderRecoversAfterRelease(t *testing.T) {
	store, err := New(t.TempDir(), 8192)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, store)
	fetch := func(context.Context) ([]byte, error) { return []byte("data"), nil }
	_, release, err := store.Get(t.Context(), "one", 0, 4, fetch)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for _, entry := range store.items {
		path = entry.path
	}
	if err = os.WriteFile(path, []byte("bad!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Get(t.Context(), "one", 0, 4, fetch); err == nil {
		t.Fatal("corrupt data served")
	}
	if store.Stats().Allocated != 4096 {
		t.Fatal("pinned corrupt entry lost its reservation")
	}
	release()
	release()
	data, again, err := store.Get(t.Context(), "one", 0, 4, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "data" {
		t.Fatal("failed to replace invalidated data")
	}
	again()
}

func TestFailedCleanupRetainsItsReservation(t *testing.T) {
	store, err := New(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, store)
	path := filepath.Join(store.dir, "blocked")
	if err = os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(path, "child")
	if err = os.WriteFile(child, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	retained, err := cleanupChunk(path, "")
	if err == nil || retained != path {
		t.Fatal("cleanup failure hidden", err)
	}
	item := &entry{done: make(chan struct{}), allocation: 4096, pins: 1}
	store.items["key"] = item
	store.used = 4096
	store.finishChunk("key", item, err, retained)
	if store.Stats().Allocated != 4096 {
		t.Fatal("failed cleanup released its reservation")
	}
	if _, _, _, err = store.acquireChunk("key", 4096); err == nil {
		t.Fatal("unremovable orphan reused")
	}
	if err = os.Remove(child); err != nil {
		t.Fatal(err)
	}
	next, created, _, err := store.acquireChunk("key", 4096)
	if err != nil || !created {
		t.Fatal(created, err)
	}
	store.finishChunk("key", next, errors.New("controlled failure"), "")
	if store.Stats().Allocated != 0 {
		t.Fatal("removed orphan remained reserved")
	}
}
