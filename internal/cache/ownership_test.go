package cache

import (
	"bytes"
	"context"
	"testing"
)

func TestClosedStoreCannotMutateReopenedOwnersPinnedChunk(t *testing.T) {
	dir := t.TempDir()

	old, err := New(dir, 2<<20)
	if err != nil {
		t.Fatal(err)
	}

	payload := bytes.Repeat([]byte{37}, 1<<20)

	_, release, err := old.Get(t.Context(), "one", 0, len(payload), func(context.Context) ([]byte, error) { return payload, nil })
	if err != nil {
		t.Fatal(err)
	}

	// A refused close retains ownership until its active reader releases.
	if err = old.Close(); err == nil {
		t.Fatal("active cache released ownership")
	}

	release()

	if err = old.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := New(dir, 2<<20)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeTest(t, current) })

	data, release, err := current.Lookup(t.Context(), "one", 0, len(payload))
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatal("reopened chunk differs", err)
	}

	defer release()

	before := old.Stats()
	for _, limit := range []int64{4096, 2 << 20, 4 << 20} {
		if err = old.Resize(limit); err == nil || old.Stats() != before {
			t.Fatal("closed cache accepted a mutation", limit, err)
		}
	}

	if _, _, err = old.Get(t.Context(), "one", 0, len(payload), func(context.Context) ([]byte, error) {
		t.Error("closed cache started a fetch")
		return nil, nil
	}); err == nil {
		t.Fatal("closed cache accepted Get")
	}

	if _, err = old.ReadCached(t.Context(), "one", 0, len(payload), make([]byte, 4), 0); err == nil {
		t.Fatal("closed cache accepted ReadCached")
	}

	if err = old.Close(); err != nil {
		t.Fatal("repeated close failed", err)
	}

	if extra, err := New(dir, 2<<20); err == nil {
		closeTest(t, extra)
		t.Fatal("old close released the new owner's lock")
	}

	data, releaseAgain, err := current.Lookup(t.Context(), "one", 0, len(payload))
	if err != nil {
		t.Fatal("stale owner removed a pinned chunk", err)
	}

	releaseAgain()

	if !bytes.Equal(data, payload) {
		t.Fatal("stale owner changed the new owner's bytes")
	}
}
