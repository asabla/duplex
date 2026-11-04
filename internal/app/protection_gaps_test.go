package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"duplex/internal/config"
	"duplex/internal/media"
	"duplex/internal/plex"
	"duplex/internal/source"
)

func TestPayloadAdmissionProtectsViewersAndDefersUnrelatedReads(t *testing.T) {
	p := config.Plex{URL: "http://plex.test:32400"}
	a := playbackFixture(t, p)
	own := "Channel-test - S2026E0001.mp4"
	other := "Channel-test - S2026E0002.mp4"

	observeIdle(a, p)

	scanner, release, err := a.startPayload(payloadContext(t.Context(), other))
	if err != nil {
		t.Fatal(err)
	}

	a.observePlayback(p, plex.Activity{Sessions: 1, MediaKeys: []string{plex.MediaKey(own)}}, nil, time.Now())

	if !errors.Is(context.Cause(scanner), errBackgroundDeferred) {
		t.Fatal("active scanner read was not interrupted", context.Cause(scanner))
	}

	release()

	viewer, release, err := a.startPayload(payloadContext(t.Context(), own))
	if err != nil || viewer.Err() != nil {
		t.Fatal("viewer blocked", err)
	}

	release()

	ctx, cancel := context.WithTimeout(payloadContext(t.Context(), other), 30*time.Millisecond)
	_, _, err = a.startPayload(ctx)

	cancel()

	if !errors.Is(err, media.ErrDeferred) {
		t.Fatal("unrelated probe bypassed protection", err)
	}

	// The next observer can authorize another actual viewer without waiting idle.
	a.observePlayback(p, plex.Activity{Sessions: 2, MediaKeys: []string{plex.MediaKey(own), plex.MediaKey(other)}}, nil, time.Now())

	second, release, err := a.startPayload(payloadContext(t.Context(), other))
	if err != nil || second.Err() != nil {
		t.Fatal("second viewer blocked", err)
	}

	release()
	// Analysis without viewers must remain able to complete its own scan.
	a.observePlayback(p, plex.Activity{Scanning: true}, nil, time.Now())

	_, release, err = a.startPayload(payloadContext(t.Context(), other))
	if err != nil {
		t.Fatal(err)
	}

	release()
}

type interruptedDiscovery struct {
	*collectionSource
	entered chan struct{}
	exited  chan struct{}
}

func (s *interruptedDiscovery) Metadata(ctx context.Context, _ string) (source.Metadata, error) {
	close(s.entered)
	<-ctx.Done()
	close(s.exited)

	return source.Metadata{}, ctx.Err()
}

func TestPlaybackCancelsDiscoveryAndRestartsWithoutFailure(t *testing.T) {
	sourceReader := &interruptedDiscovery{collectionSource: fixtureCollection(1), entered: make(chan struct{}), exited: make(chan struct{})}
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "latest", Limit: 1}), sourceReader)
	p := config.Plex{URL: "http://plex.test:32400"}
	setPlaybackConfig(t, a, p)
	observeIdle(a, p)

	done := make(chan error, 1)
	go func() { done <- a.Sync(t.Context(), testOwner) }()

	select {
	case <-sourceReader.entered:
	case <-time.After(time.Second):
		t.Fatal("discovery did not start")
	}

	a.observePlayback(p, plex.Activity{Sessions: 1}, nil, time.Now())

	select {
	case <-sourceReader.exited:
	case <-time.After(time.Second):
		t.Fatal("metadata process continued")
	}

	if err := <-done; !errors.Is(err, errBackgroundDeferred) {
		t.Fatal("interruption was not deferred", err)
	}

	jobs, err := a.Catalog.Jobs(t.Context())
	if err != nil || len(jobs) != 1 || jobs[0].State != "deferred" || jobs[0].Error != "" {
		t.Fatal(jobs, err)
	}

	var count int
	if err = a.Catalog.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM discovery_items`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial staging survived", count, err)
	}

	a.Source = sourceReader.collectionSource
	observeIdle(a, p)

	if err = a.syncWhenIdle(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	jobs, err = a.Catalog.Jobs(t.Context())
	if err != nil || len(jobs) != 2 || jobs[0].State != "complete" {
		t.Fatal("discovery did not resume", jobs, err)
	}
}
