package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"duplex/internal/cache"
	"duplex/internal/media"
)

func TestUnchangedSourceRenewalDoesNotRequestScan(t *testing.T) {
	a, adapter, _ := mediaPipeline(t)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	if err := a.prepareRecording(t.Context(), "video000000", false); err != nil {
		t.Fatal(err)
	}

	a.mu.Lock()
	a.scanPending, a.scanFull, a.scanChannels = false, false, nil
	a.mu.Unlock()

	if err := a.prepareRecording(t.Context(), "video000000", true); err != nil {
		t.Fatal(err)
	}

	if a.scanPending {
		t.Fatal("renewing an unchanged representation requested a scan")
	}

	metadata := adapter.metadata["video000000"]
	metadata.Title = "Changed recording title"

	adapter.metadata[metadata.ID] = metadata
	if err := a.prepareRecording(t.Context(), metadata.ID, true); err != nil {
		t.Fatal(err)
	}

	if !a.scanPending {
		t.Fatal("changed text metadata did not request publication")
	}
}

func TestDurableOpenHeadersAndCachedPayloadNeedNoSource(t *testing.T) {
	a, adapter, origin := mediaPipeline(t)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	if err := a.prepareRecording(t.Context(), "video000000", false); err != nil {
		t.Fatal(err)
	}

	v, err := a.Catalog.Video(t.Context(), "video000000")
	if err != nil {
		t.Fatal(err)
	}

	name := fmt.Sprintf("Channel-%s/Season %04d/Channel-%s - S%04dE%04d.mp4", testOwner, v.Year, testOwner, v.Year, v.Number)
	provider := catalogueFS{app: a}

	rep, _, err := provider.Open(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}

	expected := make([]byte, rep.Size())
	if _, err = rep.ReadAt(t.Context(), expected, 0); err != nil {
		t.Fatal(err)
	}

	index, ok := a.reps[v.Revision].(*media.VirtualMP4)
	if !ok {
		t.Fatal("fixture did not prepare a virtual MP4")
	}

	payloadOffset := int64(len(index.Header))
	a.reps = map[string]media.Representation{}
	a.metadata = map[string]metadataLease{}

	origin.expired.Store(true)
	resolves, requests := adapter.resolved, origin.requests.Load()

	local, _, err := provider.Open(t.Context(), name)
	if err != nil || adapter.resolved != resolves {
		t.Fatal("open resolved a locator", err)
	}

	data := make([]byte, len(expected))
	if _, err = local.ReadAt(t.Context(), data, 0); err != nil || !bytes.Equal(data, expected) {
		t.Fatal("local bytes changed or required a source", err)
	}

	if adapter.resolved != resolves || origin.requests.Load() != requests {
		t.Fatal("cached read contacted source")
	}

	if _, err = provider.OpenVersion(t.Context(), name, "wrong-revision"); !errors.Is(err, media.ErrChanged) {
		t.Fatal("revision was not checked", err)
	}

	// Only this owned fixture cache is replaced. Retained layouts remain usable.
	if err = a.Cache.Close(); err != nil {
		t.Fatal(err)
	}

	fresh, err := cache.New(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := fresh.Close(); err != nil {
			t.Error(err)
		}
	})

	a.Cache = fresh

	origin.expired.Store(false)

	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()

			handle, _, failure := provider.Open(t.Context(), name)
			if failure != nil {
				t.Error(failure)
				return
			}

			chunk := make([]byte, 128)
			if _, failure = handle.ReadAt(t.Context(), chunk, payloadOffset); failure != nil || !bytes.Equal(chunk, expected[payloadOffset:payloadOffset+128]) {
				t.Error("lazy payload differs", failure)
			}
		}()
	}

	readers.Wait()

	if adapter.resolved != resolves+1 {
		t.Fatal("concurrent payload reads did not share binding", adapter.resolved, resolves)
	}

	// An already canceled reader cannot force upstream work.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err = local.ReadAt(ctx, make([]byte, 1), 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLazyBindingDoesNotBelongToItsFirstReader(t *testing.T) {
	a, adapter, _ := mediaPipeline(t)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	if err := a.prepareRecording(t.Context(), "video000000", false); err != nil {
		t.Fatal(err)
	}

	v, err := a.Catalog.Video(t.Context(), "video000000")
	if err != nil {
		t.Fatal(err)
	}

	index, ok := a.reps[v.Revision].(*media.VirtualMP4)
	if !ok {
		t.Fatal("not a virtual MP4 fixture")
	}

	offset := int64(len(index.Header))
	held := &heldMetadataSource{collectionSource: adapter, started: make(chan struct{}, 4), release: make(chan struct{})}
	a.Source = held
	a.reps = map[string]media.Representation{}

	a.metadata = map[string]metadataLease{}
	if err = a.Cache.Close(); err != nil {
		t.Fatal(err)
	}

	chunks, err := cache.New(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}

	a.Cache = chunks

	t.Cleanup(func() {
		a.bindingWork.close()
		a.resolutionWork.close()

		if err := chunks.Close(); err != nil {
			t.Error(err)
		}
	})

	name := fmt.Sprintf("Channel-%s/Season %04d/Channel-%s - S%04dE%04d.mp4", testOwner, v.Year, testOwner, v.Year, v.Number)
	provider := catalogueFS{app: a}
	short, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)

	read := func(ctx context.Context) error {
		handle, _, failure := provider.Open(ctx, name)
		if failure != nil {
			return failure
		}

		_, failure = handle.ReadAt(ctx, make([]byte, 64), offset)

		return failure
	}
	go func() { first <- read(short) }()

	<-held.started

	long := make(chan error, 1)
	go func() { long <- read(t.Context()) }()

	waitSharedWaiters(t, &a.bindingWork, v.Revision, 2)
	cancel()

	if err = <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	close(held.release)

	if err = <-long; err != nil || held.calls.Load() != 1 {
		t.Fatal("first reader canceled another reader's binding", err, held.calls.Load())
	}
}
