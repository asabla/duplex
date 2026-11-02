package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/media"
	"duplex/internal/source"
)

type scanResolutionCounter struct {
	*collectionSource
	mu      sync.Mutex
	byVideo map[string]int
}

func (s *scanResolutionCounter) Resolve(ctx context.Context, id string) (source.Metadata, error) {
	s.mu.Lock()
	s.byVideo[id]++
	s.mu.Unlock()

	return s.collectionSource.Resolve(ctx, id)
}

// The sources contain independently generated, exact media. Splitting their
// existing spans preserves every output byte while exercising long-index memory
// and JSON costs without retaining hundreds of copies of third-party media.
func scanLibraryFixture(b *testing.B) (*App, *scanResolutionCounter, *mediaFixtureOrigin, []string, []byte) {
	b.Helper()

	a, adapter, origin := mediaPipeline(b)
	if err := a.Sync(b.Context(), testOwner); err != nil {
		b.Fatal(err)
	}

	m := adapter.metadata["video000000"]

	prepared, rep, err := a.prepareWithRepresentation(b.Context(), m, 2160)
	if err != nil {
		b.Fatal(err)
	}

	index, ok := rep.(*media.VirtualMP4)
	if !ok {
		b.Fatal("fixture did not prepare a virtual MP4")
	}

	template, err := json.Marshal(index)
	if err != nil {
		b.Fatal(err)
	}

	configSnapshot := a.Config.Snapshot()

	configSnapshot.Config.Channels[0].Indexing = &config.Selection{Mode: "all"}
	if err = a.Config.Save(configSnapshot.Config, configSnapshot.Revision); err != nil {
		b.Fatal(err)
	}

	paths := make([]string, 0, 500)

	for n := range 500 {
		metadata := m
		metadata.ID = fmt.Sprintf("video%06d", n)
		metadata.Title = metadata.ID
		adapter.metadata[metadata.ID] = metadata

		video, failure := a.openTrack(b.Context(), metadata.ID, metadata.Formats[0])
		if failure != nil {
			b.Fatal(failure)
		}

		audio, failure := a.openTrack(b.Context(), metadata.ID, metadata.Formats[1])
		if failure != nil {
			b.Fatal(failure)
		}

		var layout media.VirtualMP4
		if err = json.Unmarshal(template, &layout); err != nil {
			b.Fatal(err)
		}

		original := append([]string(nil), layout.Sources...)
		layout.Sources = []string{video.ID(), audio.ID()}
		layout.SourceLengths = map[string]int64{video.ID(): video.Size(), audio.ID(): audio.Size()}
		spans := make([]media.Span, 0)

		for _, span := range layout.Spans {
			oldSource := span.SourceID

			span.SourceID = video.ID()
			if oldSource == original[1] {
				span.SourceID = audio.ID()
			}

			for remaining := span.Length; remaining > 0; {
				part := span
				part.Length = min(1024, remaining)
				spans = append(spans, part)
				span.Offset += part.Length
				span.Output += part.Length
				remaining -= part.Length
			}
		}

		layout.Spans = spans
		key := sha256.Sum256([]byte(metadata.ID + ":" + metadata.Formats[0].ID + ":" + metadata.Formats[1].ID))

		indexPath := filepath.Join(a.original.IndexDir, hex.EncodeToString(key[:])+".json")
		if err = layout.SaveIndex(indexPath); err != nil {
			b.Fatal(err)
		}

		bound, failure := media.LoadIndex(indexPath, map[string]media.Representation{video.ID(): video, audio.ID(): audio})
		if failure != nil {
			b.Fatal(failure)
		}

		candidate := source.Candidate{Video: metadata.Formats[0], Audio: &metadata.Formats[1]}
		if err = a.persistRepresentation(metadata, candidate, bound, indexPath); err != nil {
			b.Fatal(err)
		}

		p := catalog.Prepared{Metadata: metadata, Revision: bound.ID(), Height: prepared.Height, RequestedHeight: 2160, Size: bound.Size()}
		if err = a.Catalog.SaveMetadata(b.Context(), metadata); err != nil {
			b.Fatal(err)
		}

		if err = a.Catalog.SavePrepared(b.Context(), p); err != nil {
			b.Fatal(err)
		}

		if err = a.Catalog.MarkUploadedSelection(b.Context(), metadata.ID); err != nil {
			b.Fatal(err)
		}

		v, failure := a.Catalog.Video(b.Context(), metadata.ID)
		if failure != nil {
			b.Fatal(failure)
		}

		paths = append(paths, fmt.Sprintf("Channel-%s/Season %04d/Channel-%s - S%04dE%04d.mp4", testOwner, v.Year, testOwner, v.Year, v.Number))
	}

	counter := &scanResolutionCounter{collectionSource: adapter, byVideo: map[string]int{}}
	a.Source = counter
	a.reps = map[string]media.Representation{}
	a.metadata = map[string]metadataLease{}

	return a, counter, origin, paths, index.Header[:64]
}

func BenchmarkLibraryScan500(b *testing.B) {
	a, counter, origin, paths, header := scanLibraryFixture(b)
	provider := catalogueFS{app: a}
	requests, received := origin.requests.Load(), origin.bytes.Load()

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	cpuBefore := scanCPUSeconds(b)
	b.ResetTimer()

	for range b.N {
		for range 2 {
			for _, name := range paths {
				rep, _, err := provider.Open(b.Context(), name)
				if err != nil {
					b.Fatal(err)
				}

				data := make([]byte, 64)
				if _, err = rep.ReadAt(b.Context(), data, 0); err != nil || !bytes.Equal(data, header) {
					b.Fatal("header differs", err)
				}
			}
		}
	}

	b.StopTimer()
	cpuAfter := scanCPUSeconds(b)

	runtime.ReadMemStats(&after)

	calls := 0
	for _, count := range counter.byVideo {
		calls += count
	}

	b.ReportMetric(float64(calls)/float64(b.N), "resolutions/op")
	b.ReportMetric(float64(len(counter.byVideo)), "unique-resolved")
	b.ReportMetric(float64(calls-len(counter.byVideo))/float64(b.N), "duplicate-resolutions/op")
	b.ReportMetric(float64(origin.requests.Load()-requests)/float64(b.N), "upstream-requests/op")
	b.ReportMetric(float64(origin.bytes.Load()-received)/float64(b.N), "upstream-bytes/op")
	b.ReportMetric(float64(after.TotalAlloc-before.TotalAlloc)/float64(b.N), "allocated-bytes/op")
	b.ReportMetric((cpuAfter-cpuBefore)/float64(b.N), "cpu-seconds/op")
}

func scanCPUSeconds(b *testing.B) float64 {
	b.Helper()

	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		b.Fatal(err)
	}

	return float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
}
