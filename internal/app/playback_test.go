package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/plex"
)

func playbackFixture(t *testing.T, p config.Plex) *App {
	t.Helper()

	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "latest", Limit: 3}), fixtureCollection(3))
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	setPlaybackConfig(t, a, p)

	return a
}

func setPlaybackConfig(t *testing.T, a *App, p config.Plex) {
	t.Helper()

	snap := a.Config.Snapshot()
	snap.Config.Plex = p

	if err := a.Config.Save(snap.Config, snap.Revision); err != nil {
		t.Fatal(err)
	}
}

func observeIdle(a *App, p config.Plex) {
	now := time.Now()
	for offset := playbackIdleGrace; offset >= 0; offset -= playbackPoll {
		a.observePlayback(p, plex.Activity{}, nil, now.Add(-offset))
	}
}

func TestPlaybackAdmissionStateFreshnessRecoveryAndConfiguration(t *testing.T) {
	p := config.Plex{URL: "http://plex.test:32400"}
	a := playbackFixture(t, p)

	if s := a.PlaybackStatus(); s.State != "checking" || !s.BackgroundPaused {
		t.Fatal("startup admitted work before a playback check", s)
	}

	now := time.Now()
	a.observePlayback(p, plex.Activity{Sessions: 1}, nil, now)

	if s := a.PlaybackStatus(); s.State != "active" || !s.BackgroundPaused || s.Sessions != 1 {
		t.Fatal(s)
	}

	a.observePlayback(p, plex.Activity{}, nil, now)

	if s := a.PlaybackStatus(); s.State != "idle" || !s.BackgroundPaused || !s.ResumeAfter.Equal(now.Add(playbackIdleGrace)) {
		t.Fatal("idle detection skipped its grace period", s)
	}

	a.mu.RLock()
	s := a.playbackStatusLocked(p, now.Add(playbackFreshness+time.Second))
	a.mu.RUnlock()

	if s.State != "unavailable" || !s.BackgroundPaused {
		t.Fatal("stale idle response admitted work", s)
	}

	a.observePlayback(p, plex.Activity{}, errors.New("private response must not escape"), now)

	if s = a.PlaybackStatus(); s.State != "unavailable" || strings.Contains(s.Reason, "private") || !s.BackgroundPaused {
		t.Fatal("failure was not safe and paused", s)
	}

	observeIdle(a, p)

	if s = a.PlaybackStatus(); s.BackgroundPaused || s.LastSuccess.IsZero() {
		t.Fatal("recovered idle connection did not resume", s)
	}

	a.observePlayback(p, plex.Activity{Scanning: true}, nil, time.Now())

	if s = a.PlaybackStatus(); s.State != "scanning" || !s.BackgroundPaused {
		t.Fatal("manual library scan did not pause preparation", s)
	}

	changed := p
	changed.TokenFile = "/new/protected-token"
	setPlaybackConfig(t, a, changed)
	a.observePlayback(p, plex.Activity{}, nil, time.Now())

	if s = a.PlaybackStatus(); s.State != "checking" || !s.BackgroundPaused {
		t.Fatal("response from the prior connection was reused", s)
	}

	disabled := false
	changed.PlaybackProtection = &disabled
	setPlaybackConfig(t, a, changed)

	if s = a.PlaybackStatus(); s.State != "disabled" || s.BackgroundPaused {
		t.Fatal(s)
	}

	setPlaybackConfig(t, a, config.Plex{})

	if s = a.PlaybackStatus(); s.State != "unconfigured" || s.BackgroundPaused {
		t.Fatal("unconfigured installations cannot make progress", s)
	}
}

type playbackTestMount struct{}

func (playbackTestMount) Ready() bool           { return true }
func (playbackTestMount) Done() <-chan struct{} { return nil }
func (playbackTestMount) Close() error          { return nil }

func TestMonitoringOnlyMakesReadOnlyRequestsWithoutNFOChecks(t *testing.T) {
	var requests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)

		if r.Method != http.MethodGet || r.URL.Path != "/status/sessions" {
			t.Error("monitoring-only connection made a library request", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)

			return
		}

		if _, err := w.Write([]byte(`<MediaContainer size="0"/>`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	disabled := false
	p := config.Plex{URL: server.URL, AutomaticUpdates: &disabled}
	a := playbackFixture(t, p)
	a.mount = playbackTestMount{}
	a.checkPlayback(t.Context())
	observeIdle(a, p)

	for _, mode := range []integrationMode{integrationHealth, integrationScan, integrationBindings} {
		if err := a.integration(t.Context(), mode); err != nil {
			t.Fatal("monitor-only connection required library prerequisites", err)
		}
	}

	a.refreshMetadataQueues(t.Context())
	a.runShowRefresh(t.Context(), catalog.ShowRefresh{})
	a.runEpisodeRefresh(t.Context(), catalog.EpisodeRefresh{})

	if err := a.refreshArtwork(t.Context(), testOwner); err != nil || requests.Load() != 1 {
		t.Fatal("monitor-only mode issued updates", requests.Load(), err)
	}

	ctx, cancel := context.WithCancel(t.Context())

	a.wg.Add(1)
	go a.playbackWorker(ctx)

	cancel()
	a.wg.Wait()
}

func TestBackgroundMetadataAdmissionReservesForegroundAndResumes(t *testing.T) {
	p := config.Plex{URL: "http://plex.test:32400"}
	a := playbackFixture(t, p)
	a.observePlayback(p, plex.Activity{Sessions: 1}, nil, time.Now())

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- a.waitBackground(ctx) }()

	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("paused admission did not cancel", err)
	}

	observeIdle(a, p)

	release, err := a.acquireBackground(t.Context(), "metadata")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel = context.WithCancel(t.Context())
	go func() { _, failure := a.acquireBackground(ctx, "metadata"); done <- failure }()

	cancel()

	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("background metadata slot was not bounded", err)
	}

	// Foreground bindings never acquire the background resolver or heavy slot.
	if _, err = a.resolveSource(t.Context(), "video000000"); err != nil {
		t.Fatal("background resolver blocked foreground playback", err)
	}

	release()

	if err = a.waitBackground(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPlaybackCancelsPreparationButAllowsDemandDrivenRenewals(t *testing.T) {
	adapter := &blockedPreparationSource{collectionSource: fixtureCollection(3), started: make(chan string, 8), cancelled: make(chan string, 8)}

	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "latest", Limit: 3}), adapter)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	p := config.Plex{URL: "http://plex.test:32400"}
	setPlaybackConfig(t, a, p)
	observeIdle(a, p)

	for range 2 {
		a.wg.Add(1)
		go a.preparationWorker(a.ctx)
	}

	t.Cleanup(func() { a.cancel(); a.wg.Wait() })
	first := nextPreparationEvent(t, adapter.started)

	a.observePlayback(p, plex.Activity{Sessions: 1}, nil, time.Now())

	if cancelled := nextPreparationEvent(t, adapter.cancelled); cancelled != first {
		t.Fatal("playback did not cancel background preparation", first, cancelled)
	}

	// An explicit preparation request remains queued during playback.
	if err := a.PrepareNow(t.Context(), "video000002"); err != nil {
		t.Fatal(err)
	}

	// An active stream's renewal has independent admission and cannot be
	// cancelled by the busy-session observation that protects that same stream.
	if err := a.Catalog.QueuePreparation(t.Context(), "video000001", streamRenewalPriority); err != nil {
		t.Fatal(err)
	}

	a.wakePreparation()

	if renewal := nextPreparationEvent(t, adapter.started); renewal != "video000001" {
		t.Fatal("nonessential preparation ran during playback", renewal)
	}

	a.observePlayback(p, plex.Activity{Sessions: 1}, nil, time.Now())

	select {
	case id := <-adapter.cancelled:
		t.Fatal("playback protection cancelled stream renewal", id)
	default:
	}

	v, err := a.Catalog.Video(t.Context(), "video000002")
	if err != nil || v.Preparation != "queued" || v.PreparationError != "" {
		t.Fatal("deferred preparation lost its durable queue state", v, err)
	}
}
