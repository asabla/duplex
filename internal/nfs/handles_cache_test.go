package nfs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"duplex/internal/library"
)

func TestHandleCacheRetainsHotEntriesWithinItsBound(t *testing.T) {
	var cache handleCache

	keys := make([][sha256.Size]byte, hotHandleLimit+1)
	for i := range keys {
		keys[i] = sha256.Sum256(fmt.Appendf(nil, "handle-%d", i))
	}

	for _, key := range keys[:hotHandleLimit] {
		id := append([]byte(nil), key[:]...)
		cache.put(record{id: id, name: "original"})
		id[0] ^= 255 // The cache must not retain a request buffer.
	}

	if r, ok := cache.get(keys[0]); !ok || [sha256.Size]byte(r.id) != keys[0] {
		t.Fatal("hot identity was not copied independently")
	}

	cache.put(record{id: keys[hotHandleLimit][:]})

	if _, ok := cache.get(keys[1]); ok {
		t.Fatal("least recently used entry was not evicted")
	}

	cache.put(record{id: keys[0][:], name: "refreshed"})

	if r, ok := cache.get(keys[0]); !ok || r.name != "refreshed" || len(cache.entries) != hotHandleLimit {
		t.Fatal("cache lost a hot entry or exceeded its bound")
	}
}

func TestCommittedHandleReadsDoNotWaitForTheDatabase(t *testing.T) {
	h, err := openHandles(t.Context(), filepath.Join(t.TempDir(), "nfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := h.db.Close(); err != nil {
			t.Error(err)
		}
	})

	r := h.describe("recording", library.Node{Revision: "original", Size: 4096})
	if err = h.save(t.Context(), []record{r}); err != nil {
		t.Fatal(err)
	}

	if _, err = h.load(t.Context(), r.id); err != nil {
		t.Fatal(err)
	}

	connection, err := h.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	loaded, err := h.load(ctx, r.id)
	if err != nil || loaded.name != r.name || loaded.info != r.info {
		t.Fatal("committed hot read waited for the reserved database connection", loaded, err)
	}

	cancel()

	if _, err = h.load(ctx, r.id); !errors.Is(err, context.Canceled) {
		t.Fatal("cached read ignored cancellation", err)
	}
}

func TestFailedHandleBatchDoesNotPopulateTheCache(t *testing.T) {
	h, err := openHandles(t.Context(), filepath.Join(t.TempDir(), "nfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := h.db.Close(); err != nil {
			t.Error(err)
		}
	})

	first := h.describe("first", library.Node{Revision: "first"})
	second := h.describe("second", library.Node{Revision: "second"})
	copy(second.id[:8], first.id[:8]) // Force a cursor collision after one insert.

	if err = h.save(t.Context(), []record{first, second}); err == nil {
		t.Fatal("conflicting wire cookies were accepted")
	}

	if _, err = h.load(t.Context(), first.id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("rolled-back identity became readable", err)
	}

	if _, ok := h.hot.get([sha256.Size]byte(first.id)); ok {
		t.Fatal("failed transaction published a cache entry")
	}
}
