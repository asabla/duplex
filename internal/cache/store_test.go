package cache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPinsReservationsAndRecovery(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, 4096)
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(context.Context) ([]byte, error) { return bytes.Repeat([]byte{7}, 100), nil }
	_, release, err := s.Get(context.Background(), "a", 0, 100, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Get(context.Background(), "b", 0, 100, fetch); !errors.Is(err, ErrFull) {
		t.Fatalf("pinned entry evicted: %v", err)
	}
	release()
	release()
	_, release, err = s.Get(context.Background(), "b", 0, 100, fetch)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err = os.WriteFile(filepath.Join(dir, ".part-crashed"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(dir, 4096)
	if err != nil {
		t.Fatal(err)
	}
	_, release, err = s.Get(context.Background(), "b", 0, 100, func(context.Context) ([]byte, error) { t.Fatal("ready chunk was lost on restart"); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err = os.Stat(filepath.Join(dir, ".part-crashed")); !os.IsNotExist(err) {
		t.Fatal("partial chunk survived recovery")
	}
}

func TestExclusiveOwnership(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := New(dir, 4096); err == nil {
		if err := other.Close(); err != nil {
			t.Error(err)
		}
		t.Fatal("two processes can own the cache")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(dir, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, s)
}

func TestConcurrentFetchDeduplication(t *testing.T) {
	s, err := New(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, release, err := s.Get(context.Background(), "a", 0, 100, func(context.Context) ([]byte, error) { count.Add(1); return make([]byte, 100), nil })
			if err != nil {
				t.Error(err)
				return
			}
			release()
		}()
	}
	close(start)
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("fetched %d times", count.Load())
	}
}

func TestFailedWriteNotPublished(t *testing.T) {
	s, err := New(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Get(context.Background(), "a", 0, 100, func(context.Context) ([]byte, error) { return make([]byte, 99), nil })
	if err == nil {
		t.Fatal("accepted partial data")
	}
	if s.Stats().Allocated != 0 || s.Stats().Chunks != 0 {
		t.Fatal("failed reservation leaked")
	}
}

func TestDefaultFiftyGiBLimitCountsUnfinishedReservations(t *testing.T) {
	store, e := New(t.TempDir(), 50<<30)
	if e != nil {
		t.Fatal(e)
	}
	defer closeTest(t, store)
	// This case tests the logical 50 GiB counter without allocating large data.
	store.space = func() (int64, int64, error) { return 1 << 50, 1 << 50, nil }
	started, finish, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	failure := errors.New("controlled interrupted fetch")
	go func() {
		_, _, err := store.Get(context.Background(), "large-reservation", 0, 26<<30, func(context.Context) ([]byte, error) { close(started); <-finish; return nil, failure })
		done <- err
	}()
	<-started
	if used := store.Stats().Allocated; used < 26<<30 || used >= 27<<30 {
		t.Fatal("unfinished reservation was not counted")
	}
	if _, _, e = store.Get(context.Background(), "another-reservation", 0, 25<<30, func(context.Context) ([]byte, error) {
		t.Error("fetch started beyond 50 GiB limit")
		return nil, failure
	}); !errors.Is(e, ErrFull) {
		t.Fatal(e)
	}
	close(finish)
	if e = <-done; !errors.Is(e, failure) {
		t.Fatal(e)
	}
	if store.Stats().Allocated != 0 {
		t.Fatal("failed reservation was not released")
	}
}

func TestExtentMetadataFitsReservedSpaceAndKeepsPins(t *testing.T) {
	const payload = 1 << 20
	store, err := New(t.TempDir(), payload+4096)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, store)
	// A fragmented filesystem reports an extent block as part of st_blocks.
	store.allocation = func(os.FileInfo) int64 { return payload + 4096 }
	data := bytes.Repeat([]byte{37}, payload)
	fetches := 0
	fetch := func(context.Context) ([]byte, error) { fetches++; return data, nil }
	got, release, err := store.Get(t.Context(), "one", 0, payload, fetch)
	if err != nil || !bytes.Equal(got, data) || store.Stats().Allocated != payload+4096 {
		t.Fatal(len(got), store.Stats(), err)
	}
	if _, _, err := store.Get(t.Context(), "two", 0, payload, fetch); !errors.Is(err, ErrFull) || fetches != 1 {
		t.Fatal("pinned extent metadata was not reserved", fetches, err)
	}
	release()
	_, release, err = store.Get(t.Context(), "two", 0, payload, fetch)
	if err != nil {
		t.Fatal(err)
	}
	release()
	got, release, err = store.Get(t.Context(), "two", 0, payload, fetch)
	if err != nil || fetches != 2 || !bytes.Equal(got, data) || store.Stats().Allocated > payload+4096 {
		t.Fatal("published chunk was not reused within its physical limit", fetches, store.Stats(), err)
	}
	release()
}

func TestPublicationReleasesUnusedExtentReservation(t *testing.T) {
	const payload = 1 << 20
	store, err := New(t.TempDir(), payload+4096)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, store)
	store.allocation = func(info os.FileInfo) int64 { return ((info.Size() + 4095) / 4096) * 4096 }
	_, release, err := store.Get(t.Context(), "large", 0, payload, func(context.Context) ([]byte, error) {
		if store.Stats().Allocated != payload+4096 {
			t.Error("unfinished allocation excludes extent metadata")
		}
		return make([]byte, payload), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if store.Stats().Allocated != payload {
		t.Fatal("unused reservation was retained", store.Stats())
	}
	_, small, err := store.Get(t.Context(), "small", 0, 4, func(context.Context) ([]byte, error) { return []byte("data"), nil })
	if err != nil {
		t.Fatal("unused reservation blocked a small read", err)
	}
	small()
}

func TestUnexpectedExtentAllocationIsNotPublished(t *testing.T) {
	const payload = 1 << 20
	store, err := New(t.TempDir(), 2*payload)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, store)
	store.allocation = func(os.FileInfo) int64 { return payload + 8192 }
	if _, _, err = store.Get(t.Context(), "large", 0, payload, func(context.Context) ([]byte, error) { return make([]byte, payload), nil }); err == nil {
		t.Fatal("allocation above the reservation was published")
	}
	if stats := store.Stats(); stats.Allocated != 0 || stats.Chunks != 0 {
		t.Fatal("failed allocation retained data", stats)
	}
}
