package source

import (
	"context"
	"time"
)

// Observed measures source operations without exposing request arguments.
type Observed struct {
	SourceAdapter
	Record func(string, time.Duration)
}

func (s Observed) elapsed(operation string) func() {
	start := time.Now()
	return func() { s.Record(operation, time.Since(start)) }
}

// ResolveChannel measures channel identity resolution.
func (s Observed) ResolveChannel(ctx context.Context, raw string) (Channel, error) {
	defer s.elapsed("source.channel")()
	return s.SourceAdapter.ResolveChannel(ctx, raw)
}

// Enumerate measures complete collection enumeration.
func (s Observed) Enumerate(ctx context.Context, id string) (Channel, error) {
	defer s.elapsed("source.enumerate")()
	return s.SourceAdapter.Enumerate(ctx, id)
}

// Metadata measures per-video metadata retrieval.
func (s Observed) Metadata(ctx context.Context, id string) (Metadata, error) {
	defer s.elapsed("source.metadata")()
	return s.SourceAdapter.Metadata(ctx, id)
}

// Resolve measures playback source resolution.
func (s Observed) Resolve(ctx context.Context, id string) (Metadata, error) {
	defer s.elapsed("source.resolve")()
	return s.SourceAdapter.Resolve(ctx, id)
}
