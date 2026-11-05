package app

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/diagnostics"
	"duplex/internal/media"
	"duplex/internal/plex"
)

func TestRejectedFreshLocatorsDelayRetriesAndAllowRecovery(t *testing.T) {
	a, adapter, origin := mediaPipeline(t)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	if err := a.prepareRecording(t.Context(), "video000000", false); err != nil {
		t.Fatal(err)
	}

	video, err := a.Catalog.Video(t.Context(), "video000000")
	if err != nil {
		t.Fatal(err)
	}

	index, ok := a.reps[video.Revision].(*media.VirtualMP4)
	if !ok {
		t.Fatal("fixture did not prepare a virtual MP4")
	}

	bound := a.cachedBinding(video.Revision)
	name := fmt.Sprintf("Channel-%s/Season %04d/Channel-%s - S%04dE%04d.mp4", testOwner, video.Year, testOwner, video.Year, video.Number)

	handle, _, err := (catalogueFS{app: a}).Open(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}

	if err = a.Cache.Resize(4096); err != nil {
		t.Fatal(err)
	}

	if err = a.Cache.Resize(16 << 20); err != nil {
		t.Fatal(err)
	}

	adapter.mu.Lock()
	before := adapter.resolved
	adapter.mu.Unlock()
	origin.expired.Store(true)

	if _, err = handle.ReadAt(t.Context(), make([]byte, 32), int64(len(index.Header))); !errors.Is(err, media.ErrExpired) {
		t.Fatal(err)
	}

	a.mu.RLock()
	failure := a.metadata[video.ID]
	a.mu.RUnlock()

	// Late readers of the same rejected lease must not erase its failure delay.
	a.rejectBinding(catalog.Episode{VideoID: video.ID, Revision: video.Revision}, bound.epoch)
	a.rejectBinding(catalog.Episode{VideoID: video.ID, Revision: video.Revision}, 0)

	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			if _, err := handle.ReadAt(t.Context(), make([]byte, 32), int64(len(index.Header))); !errors.Is(err, media.ErrExpired) {
				t.Error(err)
			}
		})
	}

	readers.Wait()
	adapter.mu.Lock()
	resolved := adapter.resolved - before
	adapter.mu.Unlock()
	a.mu.Lock()
	retained := a.metadata[video.ID]

	if resolved != 1 || !errors.Is(retained.failure, media.ErrExpired) || !retained.expires.Equal(failure.expires) {
		a.mu.Unlock()
		t.Fatal("retries bypassed or extended the failure delay", resolved, retained, failure.expires)
	}

	retained.expires = time.Now().Add(-time.Second)
	a.metadata[video.ID] = retained
	a.mu.Unlock()
	origin.expired.Store(false)

	if n, err := handle.ReadAt(t.Context(), make([]byte, 32), int64(len(index.Header))); err != nil || n != 32 {
		t.Fatal("failure delay prevented recovery", n, err)
	}

	after, err := a.Catalog.Video(t.Context(), video.ID)
	if err != nil || after.Revision != video.Revision || after.Size != video.Size || after.Updated != video.Updated {
		t.Fatal("recovery changed durable identity", after, err)
	}
}

func TestOpenVersionRetainsBindingIdentityDuringEviction(t *testing.T) {
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	a.Measurements = &diagnostics.Recorder{}
	rep := mountedBytes{id: "revision", data: []byte("fixture")}
	a.cacheRepresentation(rep, time.Now().Add(time.Hour), "video000000")

	var eviction sync.WaitGroup

	eviction.Go(func() {
		for range 10000 {
			// Simulate the atomic removal performed by runtime cache eviction.
			a.mu.Lock()
			delete(a.reps, rep.id)
			delete(a.runtimeVideos, rep.id)
			delete(a.bindingEpochs, rep.id)
			delete(a.leases, rep.id)
			a.mu.Unlock()
			a.cacheRepresentation(rep, time.Now().Add(time.Hour), "video000000")
		}
	})
	defer eviction.Wait()

	for range 10000 {
		handle, err := (catalogueFS{app: a}).OpenVersion(t.Context(), "gone.mp4", rep.id)
		if err != nil {
			continue // No cached binding and no current path is a valid outcome.
		}

		observed, ok := handle.(observedMedia)
		if !ok {
			t.Fatal("missing read measurements")
		}

		retained, ok := observed.Representation.(*lazyEpisode)
		if !ok {
			t.Fatal("missing retained episode")
		}

		if retained.episode.VideoID != "video000000" || retained.epoch == 0 {
			t.Fatal("handle lost binding identity", retained.episode, retained.epoch)
		}
	}

	eviction.Wait()
	// A retained snapshot remains complete after its map entries are removed.
	bound := a.cachedBinding(rep.id)

	a.mu.Lock()
	delete(a.runtimeVideos, rep.id)
	a.mu.Unlock()

	if bound.rep == nil || bound.videoID != "video000000" || bound.epoch == 0 {
		t.Fatal("incomplete binding snapshot", bound)
	}
}

func TestColdViewerJoinsAfterObservationWithoutHeaderSourceWork(t *testing.T) {
	a, adapter, origin := mediaPipeline(t)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	if err := a.prepareRecording(t.Context(), "video000000", false); err != nil {
		t.Fatal(err)
	}

	video, err := a.Catalog.Video(t.Context(), "video000000")
	if err != nil {
		t.Fatal(err)
	}

	index, ok := a.reps[video.Revision].(*media.VirtualMP4)
	if !ok {
		t.Fatal("fixture did not prepare a virtual MP4")
	}

	if err = a.Cache.Resize(4096); err != nil {
		t.Fatal(err)
	}

	if err = a.Cache.Resize(16 << 20); err != nil {
		t.Fatal(err)
	}

	a.mu.Lock()
	delete(a.reps, video.Revision)
	delete(a.leases, video.Revision)
	delete(a.metadata, video.ID)
	a.mu.Unlock()
	a.Measurements = &diagnostics.Recorder{}

	p := config.Plex{URL: "http://plex.test:32400"}
	setPlaybackConfig(t, a, p)

	first := "Channel-test - S2026E9999.mp4"
	name := fmt.Sprintf("Channel-%s/Season %04d/Channel-%s - S%04dE%04d.mp4", testOwner, video.Year, testOwner, video.Year, video.Number)

	a.observePlayback(p, plex.Activity{Sessions: 1, MediaKeys: []string{plex.MediaKey(first)}}, nil, time.Now())

	adapter.mu.Lock()
	resolved := adapter.resolved
	adapter.mu.Unlock()

	requests := origin.requests.Load()

	for range 100 {
		handle, _, err := (catalogueFS{app: a}).Open(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}

		if n, err := handle.ReadAt(t.Context(), make([]byte, 16), 0); err != nil || n != 16 {
			t.Fatal(n, err)
		}

		if _, err := handle.ReadAt(t.Context(), make([]byte, 16), int64(len(index.Header))); !errors.Is(err, media.ErrDeferred) {
			t.Fatal("unreported cold viewer bypassed admission", err)
		}
	}

	for _, counter := range a.Measurements.Counters() {
		if counter.Name == "layout.miss" && counter.Value != 1 {
			t.Fatal("repeated headers loaded the index again", counter.Value)
		}
	}

	adapter.mu.Lock()
	unchanged := adapter.resolved == resolved
	adapter.mu.Unlock()

	if !unchanged || origin.requests.Load() != requests {
		t.Fatal("headers or deferred reads performed source work")
	}

	// This checks admission after a session observation, not Plex's session
	// creation sequence. The latter still needs a live cold-client test.
	a.observePlayback(p, plex.Activity{Sessions: 2, MediaKeys: []string{plex.MediaKey(first), plex.MediaKey(name)}}, nil, time.Now())

	handle, _, err := (catalogueFS{app: a}).Open(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}

	if n, err := handle.ReadAt(t.Context(), make([]byte, 16), int64(len(index.Header))); err != nil || n != 16 {
		t.Fatal("observed cold viewer could not bind", n, err)
	}

	adapter.mu.Lock()
	defer adapter.mu.Unlock()

	if adapter.resolved != resolved+1 {
		t.Fatal("cold viewer did not share one resolution", adapter.resolved-resolved)
	}
}
