package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/cache"
	"duplex/internal/config"
	"duplex/internal/plex"
)

func TestScanBatchWaitsForQuietOrMaximumDelay(t *testing.T) {
	a := &App{}
	a.requestChannelScan("two")
	a.requestChannelScan("one")

	now := time.Now()

	state := scanSchedule{}
	if _, run := a.nextScan(now, &state); run {
		t.Fatal("batch did not wait for quiet")
	}

	mode, run := a.nextScan(now.Add(scanQuiet), &state)
	if !run || mode != integrationDispatch || !slices.Equal(state.targets, []string{"one", "two"}) {
		t.Fatal("changes were not coalesced", mode, run, state.targets)
	}

	state.finished(mode, errScanPending, now)
	a.recordScanState(&state)

	if status := a.scanStatusLocked(); status.State != "in_progress" || status.Pending {
		t.Fatal(status)
	}

	a.requestChannelScan("next")

	mode, run = a.nextScan(now.Add(time.Minute), &state)
	if !run || mode != integrationBindings || !a.scanPending {
		t.Fatal("publication was lost or another scan overlapped", mode, run)
	}

	state.finished(mode, nil, now)
	a.recordScanState(&state)

	if status := a.scanStatusLocked(); status.State != "queued" || !status.Pending {
		t.Fatal(status)
	}

	mode, run = a.nextScan(now.Add(time.Minute), &state)
	if !run || mode != integrationDispatch || !slices.Equal(state.targets, []string{"next"}) {
		t.Fatal("new publication did not follow completed scan", state.targets)
	}

	state = scanSchedule{}

	a.requestChannelScan("busy")
	a.scanFirst = now.Add(-scanMaximumDelay)

	a.scanLast = now
	if mode, run = a.nextScan(now, &state); !run || mode != integrationDispatch {
		t.Fatal("continuous publication postponed scan indefinitely")
	}

	a.requestScan()

	if mode, run = a.nextScan(now.Add(time.Minute), &state); !run || mode != integrationDispatch || len(state.targets) != 0 {
		t.Fatal("full request retained a narrow scope")
	}
}

func TestScanRetriesRemainPacedUntilRecovery(t *testing.T) {
	state := scanSchedule{targets: []string{"owner"}}
	a := &App{}

	now := time.Now()
	for n := range 6 {
		state.finished(integrationDispatch, errors.New("controlled prerequisite failure"), now)
		a.recordScanState(&state)

		if status := a.scanStatusLocked(); status.State != "retrying" {
			t.Fatal(status)
		}

		if _, run := a.nextScan(now.Add(time.Second), &state); run {
			t.Fatal("retry ignored backoff", n)
		}

		now = now.Add(time.Minute)
		if mode, run := a.nextScan(now, &state); !run || mode != integrationDispatch || len(state.targets) != 1 {
			t.Fatal("retry stopped or lost scope", n, state)
		}
	}

	state.finished(integrationDispatch, errScanPending, now)
	state.finished(integrationBindings, errors.New("controlled binding failure"), now)

	if mode, run := a.nextScan(now.Add(time.Minute), &state); !run || mode != integrationBindings {
		t.Fatal("binding failure caused a duplicate scan")
	}
}

func TestSharedPlaylistPublicationUsesOneSectionScan(t *testing.T) {
	channel := pipelineChannel(config.Selection{Mode: "all"})
	channel.Playlists = &config.PlaylistPolicy{Enabled: true}
	a := pipelineApp(t, channel, fixtureCollection(0))
	a.requestRecordingScan("other-uploader")

	state := scanSchedule{}

	mode, run := a.nextScan(time.Now().Add(scanQuiet), &state)
	if !run || mode != integrationDispatch || len(state.targets) != 0 {
		t.Fatal("shared recording was scanned only under its uploader", state.targets)
	}
}

type scanProtocolFixture struct {
	busy, unhealthy atomic.Bool
	badBindings     atomic.Bool
	refreshes       atomic.Int64
}

func (f *scanProtocolFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	body := "<MediaContainer/>"

	switch r.URL.Path {
	case "/identity":
		body = `<MediaContainer version="1.43.5" claimed="1"/>`
	case "/media/providers/metadata":
		body = `<MediaContainer><MetadataAgentProvider identifier="tv.plex.agents.nfo.series"><MetadataType type="2"/></MetadataAgentProvider></MediaContainer>`
	case "/library/sections":
		refreshing := 0
		if f.busy.Load() {
			refreshing = 1
		}

		body = fmt.Sprintf(`<MediaContainer><Directory key="7" agent="tv.plex.agents.nfo.series" refreshing="%d"><Location path="/plex/duplex"/></Directory></MediaContainer>`, refreshing)
	case "/:/prefs":
		body = fmt.Sprintf(`<MediaContainer><Setting id="autoEmptyTrash" value="%t"/></MediaContainer>`, f.unhealthy.Load())
	case "/library/sections/7/prefs":
		body = `<MediaContainer><Setting id="enableBIFGeneration" value="false"/><Setting id="enableIntroMarkerGeneration" value="false"/><Setting id="enableCreditsMarkerGeneration" value="false"/><Setting id="enableLoudnessAnalysis" value="false"/><Setting id="useLocalAssets" value="true"/><Setting id="useSeasonTitles" value="true"/></MediaContainer>`
	case "/library/sections/7/refresh":
		f.refreshes.Add(1)
		f.busy.Store(true)
	case "/library/sections/7/allLeaves":
		if f.badBindings.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		body = `<MediaContainer offset="0" totalSize="0"/>`
	default:
		t.Error("unexpected Plex request", r.Method, r.URL.Path)
	}

	if _, err := w.Write([]byte(body)); err != nil {
		t.Error(err)
	}
}

func TestAcceptedScanOutlivesRequestContextAndRecoversPrerequisites(t *testing.T) {
	f := &scanProtocolFixture{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.serve(t, w, r) }))
	defer server.Close()

	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	a.mount = playbackTestMount{}
	setPlaybackConfig(t, a, config.Plex{URL: server.URL, Section: "7", PlaybackProtection: new(bool)})
	f.unhealthy.Store(true)

	if err := a.integration(t.Context(), integrationHealth); err == nil || a.plexBindingError != "" {
		t.Fatal("prerequisite failure was classified as a binding failure", err)
	}

	f.unhealthy.Store(false)

	if err := a.integration(t.Context(), integrationHealth); err != nil || a.plexError != "" {
		t.Fatal("corrected prerequisite remained stale", err)
	}

	f.badBindings.Store(true)

	if err := a.integration(t.Context(), integrationBindings); err == nil || a.plexBindingError == "" {
		t.Fatal("binding failure was not retained", err)
	}

	if err := a.integration(t.Context(), integrationHealth); err != nil || a.plexError != "" || a.plexBindingError == "" {
		t.Fatal("health changed an actual binding failure", err)
	}

	mediaCache, err := cache.New(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeTest(t, mediaCache) })

	a.Cache = mediaCache

	status, _, err := a.Summary(t.Context())
	if err != nil || status.PlexError == "" {
		t.Fatal("binding failure disappeared from diagnostics", err)
	}

	f.badBindings.Store(false)

	if err = a.integration(t.Context(), integrationBindings); err != nil || a.plexBindingError != "" {
		t.Fatal("successful reconciliation retained a binding failure", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	err = a.integrationTargets(ctx, integrationDispatch, []string{testOwner})

	cancel()

	if !errors.Is(err, errScanPending) || f.refreshes.Load() != 1 || a.plexError != "" {
		t.Fatal("accepted scan was treated as failed", err)
	}

	state := scanSchedule{}
	now := time.Now()
	state.finished(integrationDispatch, err, now)
	a.requestChannelScan("later")

	for range 5 {
		now = now.Add(time.Minute)

		mode, run := a.nextScan(now, &state)
		if !run || mode != integrationBindings {
			t.Fatal("long scan was resubmitted", mode, run)
		}

		err = a.integration(t.Context(), mode)
		if !errors.Is(err, errScanPending) || a.plexError != "" {
			t.Fatal("busy scanner became a service failure", err)
		}

		state.finished(mode, err, now)
	}

	f.busy.Store(false)

	err = a.integration(t.Context(), integrationBindings)
	state.finished(integrationBindings, err, now)

	if err != nil || state.awaiting || !a.scanPending || f.refreshes.Load() != 1 {
		t.Fatal("scan did not reconcile or lost pending publication", err, state)
	}
}

func TestPlexAnalysisKeepsBackgroundWorkPaused(t *testing.T) {
	p := config.Plex{URL: "http://plex.test:32400"}
	a := playbackFixture(t, p)
	observeIdle(a, p)
	a.observePlayback(p, plex.Activity{Tasks: 1}, nil, time.Now())

	if s := a.PlaybackStatus(); s.State != "processing" || !s.BackgroundPaused || s.BackgroundTasks != 1 {
		t.Fatal("analysis without scanner flag admitted background work", s)
	}

	a.observePlayback(p, plex.Activity{}, nil, time.Now())

	if s := a.PlaybackStatus(); !s.BackgroundPaused {
		t.Fatal("analysis completion skipped idle grace", s)
	}
}

func TestHistoricalBindingFailureDoesNotBlockMetadataQueue(t *testing.T) {
	f := &scanProtocolFixture{}
	refreshed := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/library/sections/7/all":
			if _, err := fmt.Fprintf(w, `<MediaContainer><Directory ratingKey="42" guid="tv.plex.agents.nfo.series://show/youtube-channel%%3A%s"/></MediaContainer>`, testOwner); err != nil {
				t.Error(err)
			}
		case "/library/metadata/42/refresh":
			if r.Method != http.MethodPut {
				t.Error("incorrect metadata request method", r.Method)
			}

			refreshed <- struct{}{}
		default:
			f.serve(t, w, r)
		}
	}))
	defer server.Close()

	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	a.mount = playbackTestMount{}
	setPlaybackConfig(t, a, config.Plex{URL: server.URL, Section: "7", PlaybackProtection: new(bool)})

	if err := a.Catalog.EnsureChannel(t.Context(), testOwner, "Controlled channel"); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Catalog.RecordShowMetadata(t.Context(), testOwner, "changed-show", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	a.plexBindingError = "controlled historical binding failure"
	ctx, cancel := context.WithCancel(t.Context())

	a.wg.Add(1)
	go a.metadataWorker(ctx)

	t.Cleanup(func() { cancel(); a.wg.Wait() })

	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("historical binding failure blocked the metadata queue")
	}

	cancel()
	a.wg.Wait()

	if a.plexBindingError == "" {
		t.Fatal("metadata work cleared an unrelated binding observation")
	}
}
