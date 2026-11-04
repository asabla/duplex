package cache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestCachedRangesRetainVerifiedBytesAndBoundMemory(t *testing.T) {
	store, err := New(t.TempDir(), 40<<20)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})

	payload := bytes.Repeat([]byte{37}, 1<<20)

	_, release, err := store.Get(t.Context(), "one", 0, len(payload), func(context.Context) ([]byte, error) { return bytes.Clone(payload), nil })
	if err != nil {
		t.Fatal(err)
	}

	release()

	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()

			data := make([]byte, 4)
			if n, err := store.ReadCached(t.Context(), "one", 0, len(payload), data, 71); err != nil || n != 4 || !bytes.Equal(data, payload[:4]) {
				t.Error(n, err, data)
			}
		}()
	}

	readers.Wait()

	key := chunkKey("one", 0, len(payload))
	if len(store.hot) != 1 || store.hotBytes != int64(len(payload)+1) {
		t.Fatal("snapshot accounting", len(store.hot), store.hotBytes)
	}

	// Callers of the existing full-buffer API cannot modify a shared snapshot.
	data, release, err := store.Lookup(t.Context(), "one", 0, len(payload))
	if err != nil {
		t.Fatal(err)
	}

	data[71] = 99

	release()

	small := make([]byte, 4)
	if _, err = store.ReadCached(t.Context(), "one", 0, len(payload), small, 71); err != nil || small[0] != 37 {
		t.Fatal("mutable buffer escaped", small, err)
	}

	// A damaged disk copy cannot replace bytes in an already verified snapshot.
	if err = os.WriteFile(store.items[key].path, make([]byte, len(payload)), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err = store.ReadCached(t.Context(), "one", 0, len(payload), small, 71); err != nil || small[0] != 37 {
		t.Fatal(small, err)
	}

	if err = store.Resize(4096); err != nil {
		t.Fatal(err)
	}

	if len(store.hot) != 0 || store.hotBytes != 0 {
		t.Fatal("eviction retained snapshot")
	}

	if _, err = store.ReadCached(t.Context(), "one", 0, len(payload), small, 0); !errors.Is(err, ErrMiss) {
		t.Fatal(err)
	}

	if err = store.Resize(40 << 20); err != nil {
		t.Fatal(err)
	}

	for i := range 36 {
		id := string(rune('a' + i))

		_, release, err = store.Get(t.Context(), id, 0, len(payload), func(context.Context) ([]byte, error) { return bytes.Clone(payload), nil })
		if err != nil {
			t.Fatal(err)
		}

		release()

		if _, err = store.ReadCached(t.Context(), id, 0, len(payload), small, 0); err != nil {
			t.Fatal(err)
		}
	}

	if store.hotBytes > 32<<20 || len(store.hot) > 128 {
		t.Fatal("unbounded snapshot cache", store.hotBytes, len(store.hot))
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err = store.ReadCached(ctx, "z", 0, len(payload), small, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if _, err = store.ReadCached(t.Context(), "z", 0, 4, small, 4); err == nil {
		t.Fatal("invalid range accepted")
	}
}

func TestCachedRangeRejectsCorruptionBeforeFirstSnapshot(t *testing.T) {
	dir := t.TempDir()

	store, err := New(dir, 2<<20)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})

	_, release, err := store.Get(t.Context(), "bad", 0, 32, func(context.Context) ([]byte, error) { return make([]byte, 32), nil })
	if err != nil {
		t.Fatal(err)
	}

	release()

	files, err := filepath.Glob(filepath.Join(dir, "*.chunk"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}

	if err = os.WriteFile(files[0], bytes.Repeat([]byte{1}, 32), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err = store.ReadCached(t.Context(), "bad", 0, 32, make([]byte, 4), 0); err == nil {
		t.Fatal("corrupt bytes returned")
	}

	if len(store.hot) != 0 || store.Stats().Chunks != 0 {
		t.Fatal("corrupt snapshot retained")
	}
}
