package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/config"
	"duplex/internal/plex"
	"duplex/internal/source"
)

func TestMonitoringGapRestartsIdleGrace(t *testing.T) {
	p := config.Plex{URL: "http://plex.test:32400"}
	a := playbackFixture(t, p)
	observeIdle(a, p)

	if a.PlaybackStatus().BackgroundPaused {
		t.Fatal("fixture did not reach idle")
	}

	now := time.Now().Add(playbackFreshness + time.Second)
	a.observePlayback(p, plex.Activity{}, nil, now)

	a.mu.RLock()
	s := a.playbackStatusLocked(p, now)
	a.mu.RUnlock()

	if !s.BackgroundPaused || !s.ResumeAfter.Equal(now.Add(playbackIdleGrace)) {
		t.Fatal("a monitoring gap counted as confirmed idle", s)
	}
}

func TestMonitorCancellationStopsRequestAndKeepsWorkPaused(t *testing.T) {
	started := make(chan struct{}, 1)

	var requests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requests.Add(1)

		started <- struct{}{}

		<-r.Context().Done()
	}))
	defer server.Close()

	p := config.Plex{URL: server.URL}
	a := playbackFixture(t, p)
	ctx, cancel := context.WithCancel(t.Context())

	a.wg.Add(1)

	go a.playbackWorker(ctx)

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("monitor did not request status")
	}

	cancel()
	a.wg.Wait()

	if s := a.PlaybackStatus(); s.State != "unavailable" || !s.BackgroundPaused || requests.Load() != 1 {
		t.Fatal("canceled monitoring admitted work or made overlapping requests", s, requests.Load())
	}
}

func TestCachedReadsDoNotPromoteRenewalsDuringPlayback(t *testing.T) {
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	prepared := projectionVideo(t, a, "video000001", 1)
	projectionVideo(t, a, "video000002", 2)

	rep := mountedBytes{id: prepared.Revision, data: []byte("immutable bytes!")}
	a.cacheRepresentation(rep, time.Now().Add(3*time.Minute), prepared.Metadata.ID)
	provider := catalogueFS{app: a}
	observed := provider.observedFile(rep, "recording.mp4")

	_, err := observed.ReadAt(t.Context(), make([]byte, 4), 0)
	if err != nil {
		t.Fatal(err)
	}

	p := config.Plex{URL: "http://plex.test:32400"}
	setPlaybackConfig(t, a, p)
	a.observePlayback(p, plex.Activity{Sessions: 1}, nil, time.Now())
	a.renewCachedSources(t.Context())

	if task, _, failure := a.nextPreparation(t.Context()); !errors.Is(failure, sql.ErrNoRows) {
		t.Fatal("a cache read bypassed playback protection", task, failure)
	}

	if err = a.Catalog.QueuePreparation(t.Context(), "video000002", 1000); err != nil {
		t.Fatal(err)
	}

	if _, _, err = a.nextPreparation(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("manual preparation bypassed playback protection", err)
	}
}

func TestDiscoveryAdmissionCancelsWhilePausedAndResumes(t *testing.T) {
	p := config.Plex{URL: "http://plex.test:32400"}
	a := playbackFixture(t, p)
	reader := admittedCollections{app: a, Collections: fixtureCollection(1)}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	called := false
	consume := func([]source.Entry) error { called = true; return nil }

	if _, err := reader.Entries(ctx, source.Scope{ChannelID: testOwner, Tab: "videos"}, consume); !errors.Is(err, context.Canceled) || called {
		t.Fatal("paused discovery reached the source", err, called)
	}

	observeIdle(a, p)

	if _, err := reader.Entries(t.Context(), source.Scope{ChannelID: testOwner, Tab: "videos"}, consume); err != nil || !called {
		t.Fatal("discovery did not resume", err, called)
	}
}

func TestDeferredScanRetainsPendingWorkWithoutServiceFailure(t *testing.T) {
	p := config.Plex{URL: "http://plex.test:32400"}
	a := playbackFixture(t, p)
	a.mount = playbackTestMount{}

	if err := a.integration(t.Context(), integrationScan); !errors.Is(err, errBackgroundDeferred) {
		t.Fatal("scan bypassed startup protection", err)
	}

	a.integrationResult(integrationScan, errBackgroundDeferred)
	a.mu.RLock()
	pending, serviceError, bindingError := a.scanPending, a.plexError, a.plexBindingError
	a.mu.RUnlock()

	if !pending || serviceError != "" || bindingError != "" {
		t.Fatal("deferred scan was lost or reported as a fault", pending, serviceError, bindingError)
	}

	// The automatic worker owns retries after consuming its publication batch.
	a.mu.Lock()
	a.scanPending = false
	a.mu.Unlock()

	err := a.integration(t.Context(), integrationDispatch)
	if !errors.Is(err, errBackgroundDeferred) || a.scanPending {
		t.Fatal("deferred worker duplicated its owned batch", err, a.scanPending)
	}

	state := scanSchedule{}
	now := time.Now()
	state.finished(integrationDispatch, err, now)

	if mode, run := a.nextScan(now.Add(time.Minute), &state); !run || mode != integrationDispatch {
		t.Fatal("worker lost its deferred batch")
	}
}
