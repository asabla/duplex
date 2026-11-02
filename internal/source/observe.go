package source

import (
	"context"
	"time"
)

// Observed measures source operations without exposing request arguments.
type Observed struct {
	SourceAdapter
	Record func(string, time.Duration)
	Count  func(string, int64)
}

func (s Observed) elapsed(operation string) func() {
	start := time.Now()

	return func() {
		if s.Record != nil {
			s.Record(operation, time.Since(start))
		}
	}
}

// ResolveChannel measures channel identity resolution.
func (s Observed) ResolveChannel(ctx context.Context, raw string) (Channel, error) {
	defer s.elapsed("source.channel")()
	return s.SourceAdapter.ResolveChannel(s.processContext(ctx, CauseDiscovery), raw)
}

// Enumerate measures complete collection enumeration.
func (s Observed) Enumerate(ctx context.Context, id string) (Channel, error) {
	defer s.elapsed("source.enumerate")()
	return s.SourceAdapter.Enumerate(s.processContext(ctx, CauseDiscovery), id)
}

// Metadata measures per-video metadata retrieval.
func (s Observed) Metadata(ctx context.Context, id string) (Metadata, error) {
	defer s.elapsed("source.metadata")()
	return s.SourceAdapter.Metadata(s.processContext(ctx, CauseDiscovery), id)
}

// Resolve measures playback source resolution.
func (s Observed) Resolve(ctx context.Context, id string) (Metadata, error) {
	defer s.elapsed("source.resolve")()
	return s.SourceAdapter.Resolve(s.processContext(ctx, CauseUnspecified), id)
}

type observedCollections struct {
	Collections
	observed Observed
}

func (s observedCollections) Entries(ctx context.Context, scope Scope, consume func([]Entry) error) (Listing, error) {
	defer s.observed.elapsed("source.entries")()
	return s.Collections.Entries(s.observed.processContext(ctx, CauseDiscovery), scope, consume)
}

func (s observedCollections) Playlists(ctx context.Context, channel string) ([]Playlist, error) {
	defer s.observed.elapsed("source.playlists")()
	return s.Collections.Playlists(s.observed.processContext(ctx, CauseDiscovery), channel)
}

func (s observedCollections) StreamPlaylists(ctx context.Context, channel string, consume func([]Playlist) error) error {
	ctx = s.observed.processContext(ctx, CauseDiscovery)
	defer s.observed.elapsed("source.playlists")()

	if stream, ok := s.Collections.(PlaylistStreamer); ok {
		return stream.StreamPlaylists(ctx, channel, consume)
	}

	playlists, err := s.Collections.Playlists(ctx, channel)
	if err != nil {
		return err
	}

	return consume(playlists)
}
