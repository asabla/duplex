package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"duplex/internal/cache"
	"duplex/internal/media"
	"duplex/internal/source"
)

type delayedPlaybackSource struct {
	*collectionSource
	delay time.Duration
}

func (s delayedPlaybackSource) Resolve(ctx context.Context, id string) (source.Metadata, error) {
	timer := time.NewTimer(s.delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return source.Metadata{}, ctx.Err()
	case <-timer.C:
		return s.collectionSource.Resolve(ctx, id)
	}
}

// Each sample retains verified indexes, empties runtime bindings and media
// chunks, runs restart queue work, then opens and reads the first media bytes.
// Report restart work separately: warming moves source work before the open.
func BenchmarkRetainedStartupOpen(b *testing.B) {
	a, adapter, origin := mediaPipeline(b)
	if err := a.Sync(b.Context(), testOwner); err != nil {
		b.Fatal(err)
	}

	if err := a.prepareRecording(b.Context(), "video000000", false); err != nil {
		b.Fatal(err)
	}

	video, err := a.Catalog.Video(b.Context(), "video000000")
	if err != nil {
		b.Fatal(err)
	}

	if err = a.Cache.Close(); err != nil {
		b.Fatal(err)
	}

	a.Cache, err = cache.New(b.TempDir(), 16<<20)
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		if err := a.Cache.Close(); err != nil {
			b.Error(err)
		}
	})

	a.reps = map[string]media.Representation{}
	a.metadata = map[string]metadataLease{}
	a.Source = delayedPlaybackSource{collectionSource: adapter, delay: 250 * time.Millisecond}
	filename := fmt.Sprintf("Channel-%s/Season %04d/Channel-%s - S%04dE%04d.mp4", testOwner, video.Year, testOwner, video.Year, video.Number)
	provider := catalogueFS{app: a}
	iterations := 0
	requests, transferred, resolves := origin.requests.Load(), origin.bytes.Load(), adapter.resolved

	for b.Loop() {
		if iterations > 0 {
			b.Fatal("use -benchtime=1x for retained startup")
		}

		iterations++
		started := time.Now()

		if err = a.Catalog.ResetPreparations(b.Context(), visibility(a.Config.Snapshot().Config)); err != nil {
			b.Fatal(err)
		}

		warmed := 0

		for {
			task, claimErr := a.Catalog.ClaimPreparationTask(b.Context())
			if errors.Is(claimErr, sql.ErrNoRows) {
				break
			}

			if claimErr != nil {
				b.Fatal(claimErr)
			}

			a.runPreparation(b.Context(), task)

			warmed++
		}

		warmTime := time.Since(started)
		opened := time.Now()

		representation, _, openErr := provider.Open(b.Context(), filename)
		if openErr != nil {
			b.Fatal(openErr)
		}

		data := make([]byte, 64<<10)
		if _, err = representation.ReadAt(b.Context(), data, 0); err != nil {
			b.Fatal(err)
		}

		b.ReportMetric(float64(time.Since(opened).Nanoseconds()), "open-read-ns")
		b.ReportMetric(float64(warmTime.Nanoseconds()), "restart-work-ns")
		b.ReportMetric(float64(warmed), "warmed-recordings")
		b.ReportMetric(float64(origin.requests.Load()-requests), "source-requests")
		b.ReportMetric(float64(origin.bytes.Load()-transferred), "source-bytes")
		b.ReportMetric(float64(adapter.resolved-resolves), "resolver-calls")
	}
}
