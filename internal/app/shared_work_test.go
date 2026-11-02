package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/config"
	"duplex/internal/source"
)

type heldMetadataSource struct {
	*collectionSource
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
	failed  atomic.Bool
}

func (s *heldMetadataSource) Resolve(ctx context.Context, id string) (source.Metadata, error) {
	s.calls.Add(1)

	s.started <- struct{}{}

	select {
	case <-ctx.Done():
		return source.Metadata{}, ctx.Err()
	case <-s.release:
	}

	if s.failed.Load() {
		return source.Metadata{}, source.ErrUnavailable
	}

	return s.collectionSource.Resolve(ctx, id)
}

func sharedMetadataFixture(t *testing.T) (*App, *heldMetadataSource) {
	t.Helper()

	adapter := &heldMetadataSource{collectionSource: fixtureCollection(1), started: make(chan struct{}, 32), release: make(chan struct{})}
	m := adapter.metadata["video000000"]
	m.Formats = []source.Format{{ID: "video", URL: "https://r1.googlevideo.com/video"}}
	adapter.metadata[m.ID] = m
	a := pipelineApp(t, pipelineChannel(config.DefaultSelection()), adapter)
	t.Cleanup(func() { a.bindingWork.close(); a.resolutionWork.close() })

	return a, adapter
}

func waitSharedWaiters[T any](t *testing.T, g *sharedWork[T], key string, count int) {
	t.Helper()

	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()

	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()

	for {
		g.mu.Lock()
		job := g.jobs[key]
		ready := job != nil && job.waiters == count
		g.mu.Unlock()

		if ready {
			return
		}

		select {
		case <-deadline.C:
			t.Fatal("shared waiters did not join")
		case <-tick.C:
		}
	}
}

func TestSharedResolutionKeepsRemainingWaiterAndForcedRenewal(t *testing.T) {
	a, adapter := sharedMetadataFixture(t)
	short, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)

	go func() { _, err := a.playbackMetadata(short, "video000000", false); first <- err }()

	<-adapter.started

	long := make(chan error, 1)

	go func() { _, err := a.playbackMetadata(t.Context(), "video000000", false); long <- err }()

	waitSharedWaiters(t, &a.resolutionWork, "video000000", 2)
	cancel()

	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	close(adapter.release)

	if err := <-long; err != nil || adapter.calls.Load() != 1 {
		t.Fatal("short waiter canceled shared work", err, adapter.calls.Load())
	}

	if _, err := a.playbackMetadata(t.Context(), "video000000", false); err != nil || adapter.calls.Load() != 1 {
		t.Fatal("fresh cache missed", err, adapter.calls.Load())
	}

	if _, err := a.playbackMetadata(t.Context(), "video000000", true); err != nil || adapter.calls.Load() != 2 {
		t.Fatal("forced renewal reused a stale result", err, adapter.calls.Load())
	}
}

func TestSharedWorkCancelsOrphansAndHasBoundedAdmission(t *testing.T) {
	var group sharedWork[int]

	owner, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	t.Cleanup(group.close)

	started := make(chan struct{}, maxSharedWork)

	var waiters sync.WaitGroup
	for n := range maxSharedWork {
		waiters.Add(1)
		go func() {
			defer waiters.Done()

			_, _, failure := group.do(t.Context(), owner, fmt.Sprint(n), func(ctx context.Context) (int, error) { started <- struct{}{}; <-ctx.Done(); return 0, ctx.Err() })
			if !errors.Is(failure, context.Canceled) {
				t.Error(failure)
			}
		}()
	}

	for range maxSharedWork {
		<-started
	}

	if _, _, err := group.do(t.Context(), owner, "overflow", func(context.Context) (int, error) { t.Error("capacity exceeded"); return 0, nil }); err == nil {
		t.Fatal("source work was unbounded")
	}

	cancel()
	waiters.Wait()
	group.close()

	if _, _, err := group.do(t.Context(), owner, "closed", func(context.Context) (int, error) { return 1, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal("shutdown admitted source work", err)
	}

	var orphan sharedWork[int]

	ctx, stop := context.WithCancel(t.Context())
	running := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		_, _, failure := orphan.do(ctx, t.Context(), "orphan", func(work context.Context) (int, error) {
			close(running)
			<-work.Done()
			close(stopped)

			return 0, work.Err()
		})
		if !errors.Is(failure, context.Canceled) {
			t.Error(failure)
		}
	}()

	<-running
	stop()
	<-stopped
	orphan.close()
}

func TestResolutionCacheUsesExpiryLRUByteBudgetAndFailureRecovery(t *testing.T) {
	a, adapter := sharedMetadataFixture(t)
	close(adapter.release)

	for n := range metadataEntries {
		m := adapter.metadata["video000000"]
		m.ID = fmt.Sprint(n)
		a.remember(m)
	}

	if _, ok, err := a.cachedMetadata("0"); err != nil || !ok {
		t.Fatal("fixture cache missing")
	}

	m := adapter.metadata["video000000"]
	m.ID = "new"
	a.remember(m)

	if _, ok, err := a.cachedMetadata("0"); err != nil || !ok {
		t.Fatal("recently used result was evicted")
	}

	if _, ok, err := a.cachedMetadata("1"); err != nil || ok {
		t.Fatal("least recently used result was retained")
	}

	for n := range 10 {
		m.ID = fmt.Sprint(n)
		m.Description = strings.Repeat("x", 1<<20)
		a.remember(m)
	}

	cost := 0
	for _, lease := range a.metadata {
		cost += lease.bytes
	}

	if cost > metadataBudget || len(a.metadata) > metadataEntries {
		t.Fatal("metadata cache exceeded bounds", cost, len(a.metadata))
	}

	adapter.failed.Store(true)

	if _, err := a.playbackMetadata(t.Context(), "video000000", true); !errors.Is(err, source.ErrUnavailable) {
		t.Fatal(err)
	}

	if _, err := a.playbackMetadata(t.Context(), "video000000", false); !errors.Is(err, source.ErrUnavailable) || adapter.calls.Load() != 1 {
		t.Fatal("failure storm was not paced", err, adapter.calls.Load())
	}

	adapter.failed.Store(false)
	a.mu.Lock()
	lease := a.metadata["video000000"]
	lease.expires = time.Now().Add(-time.Second)
	a.metadata["video000000"] = lease
	a.mu.Unlock()

	if _, err := a.playbackMetadata(t.Context(), "video000000", false); err != nil || adapter.calls.Load() != 2 {
		t.Fatal("failure backoff hid recovery", err, adapter.calls.Load())
	}

	m = adapter.metadata["video000000"]
	m.ID = "expired"
	m.Formats = []source.Format{{URL: "https://r1.googlevideo.com/video?expire=1"}}
	a.remember(m)

	if _, ok, err := a.cachedMetadata(m.ID); err != nil || ok {
		t.Fatal("expired locator was reused")
	}
}
