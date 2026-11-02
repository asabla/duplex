package cache

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestLookupOnlyReturnsVerifiedPublishedPinnedBytes(t *testing.T) {
	s, err := New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	if _, _, err = s.Lookup(t.Context(), "source", 0, 8); !errors.Is(err, ErrMiss) || s.Stats().Allocated != 0 {
		t.Fatal("lookup allocated or fetched a missing chunk", err)
	}

	_, release, err := s.Get(t.Context(), "source", 0, 8, func(context.Context) ([]byte, error) { return []byte("original"), nil })
	if err != nil {
		t.Fatal(err)
	}

	release()

	data, unpin, err := s.Lookup(t.Context(), "source", 0, 8)
	if err != nil || string(data) != "original" {
		t.Fatal(string(data), err)
	}

	item := s.items[chunkKey("source", 0, 8)]
	if item.pins != 1 {
		t.Fatal("local bytes were not pinned")
	}

	unpin()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, _, err = s.Lookup(ctx, "source", 0, 8); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if err = os.WriteFile(item.path, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err = s.Lookup(t.Context(), "source", 0, 8); err == nil {
		t.Fatal("corrupt bytes were returned")
	}

	if s.Stats().Allocated != 0 {
		t.Fatal("corrupt entry retained allocation")
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	if _, _, err = s.Lookup(t.Context(), "source", 0, 8); err == nil {
		t.Fatal("closed cache returned bytes")
	}
}
