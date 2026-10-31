package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/media"
	"duplex/internal/source"
)

type scanAssetTransport struct {
	base     assetFixtureTransport
	delay    time.Duration
	requests atomic.Int64
}

func (f *scanAssetTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.requests.Add(1)

	if f.delay > 0 {
		timer := time.NewTimer(f.delay)
		defer timer.Stop()

		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-timer.C:
		}
	}

	return f.base.RoundTrip(r)
}

func scanMediaPipeline(b *testing.B, delay time.Duration, failed bool) (*App, *collectionSource, *mediaFixtureOrigin, *scanAssetTransport) {
	b.Helper()

	a, adapter, origin := mediaPipeline(b)
	setCaptionFetching(b, a, true)

	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 128, 72))); err != nil {
		b.Fatal(err)
	}

	status := http.StatusOK
	if failed {
		status = http.StatusServiceUnavailable
	}

	transport := &scanAssetTransport{delay: delay, base: assetFixtureTransport{
		"/scan-poster":   {data: picture.Bytes(), status: status},
		"/scan-captions": {data: []byte("1\n00:00:00,000 --> 00:00:01,000\nControlled original captions\n"), status: status},
	}}
	previous := http.DefaultTransport
	http.DefaultTransport = transport

	b.Cleanup(func() { http.DefaultTransport = previous })

	m := adapter.metadata["video000000"]
	m.Thumbnail = "https://i.ytimg.com/scan-poster"
	m.Subtitles = map[string][]source.Subtitle{"en": {{Ext: "srt", URL: "https://www.youtube.com/scan-captions"}}}
	adapter.metadata[m.ID] = m

	if err := a.Sync(b.Context(), testOwner); err != nil {
		b.Fatal(err)
	}

	// Isolate the scan requested for the first prepared recording from the
	// earlier discovery notification, which can contain no playable files.
	select {
	case <-a.scanWake:
	default:
	}

	a.mu.Lock()
	a.scanPending = false
	a.mu.Unlock()

	return a, adapter, origin, transport
}

// Fixed one-iteration samples create a new channel and asset queue each time.
// Run with -benchtime=1x -count=10 and controlled media fixtures.
func BenchmarkInitialScanArtifacts(b *testing.B) {
	for _, scenario := range []struct {
		name   string
		delay  time.Duration
		failed bool
	}{{"available", 25 * time.Millisecond, false}, {"slow", 500 * time.Millisecond, false}, {"unavailable", 0, true}} {
		b.Run(scenario.name, func(b *testing.B) {
			a, _, _, transport := scanMediaPipeline(b, scenario.delay, scenario.failed)
			ctx, cancel := context.WithCancel(b.Context())

			a.wg.Add(1)
			go a.assetWorker(ctx)

			b.Cleanup(func() { cancel(); a.wg.Wait() })

			iterations := 0
			for b.Loop() {
				if iterations > 0 {
					b.Fatal("use -benchtime=1x for initial channel readiness")
				}

				iterations++
				started := time.Now()

				if err := a.prepareRecording(b.Context(), "video000000", false); err != nil {
					b.Fatal(err)
				}

				prepared := time.Since(started)

				select {
				case <-a.scanWake:
				default:
					b.Fatal("media preparation did not request its first scan")
				}

				atScan := len(a.loadAssets("video000000"))
				deadline := time.Now().Add(10 * time.Second)

				for {
					var state string
					if err := a.Catalog.DB.QueryRowContext(b.Context(), `SELECT state FROM asset_jobs WHERE video_id='video000000'`).Scan(&state); err != nil {
						b.Fatal(err)
					}

					if state == "ready" || state == "failed" {
						break
					}

					if time.Now().After(deadline) {
						b.Fatal("asset work did not complete")
					}

					time.Sleep(time.Millisecond)
				}

				b.ReportMetric(float64(prepared.Nanoseconds()), "prepare-ns")
				b.ReportMetric(float64(time.Since(started).Nanoseconds()), "assets-finished-ns")
				b.ReportMetric(float64(atScan), "assets-at-scan")
				b.ReportMetric(float64(len(a.loadAssets("video000000"))), "assets-finished")
				b.ReportMetric(float64(transport.requests.Load()), "asset-requests")
			}
		})
	}
}

func BenchmarkMediaOpenState(b *testing.B) {
	for _, cold := range []bool{false, true} {
		b.Run(fmt.Sprintf("unloaded_%t", cold), func(b *testing.B) {
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

			name := fmt.Sprintf("Channel-%s/Season %04d/Channel-%s - S%04dE%04d.mp4", testOwner, video.Year, testOwner, video.Year, video.Number)
			provider := catalogueFS{app: a}
			requests := origin.requests.Load()
			resolves := adapter.resolved
			iterations := 0
			replacements := 0

			for b.Loop() {
				var previous map[string]os.FileInfo

				if cold {
					b.StopTimer()
					previous = benchmarkIndexFiles(b, a.original.IndexDir)
					a.mu.Lock()
					a.reps = map[string]media.Representation{}
					a.metadata = map[string]metadataLease{}
					a.mu.Unlock()
					b.StartTimer()
				}

				if _, _, err := provider.Open(b.Context(), filepath.ToSlash(name)); err != nil {
					b.Fatal(err)
				}

				if cold {
					b.StopTimer()

					for file, current := range benchmarkIndexFiles(b, a.original.IndexDir) {
						if old, exists := previous[file]; exists && !os.SameFile(old, current) {
							replacements++
						}
					}

					b.StartTimer()
				}

				iterations++
			}

			b.ReportMetric(float64(origin.requests.Load()-requests)/float64(iterations), "source-requests/op")
			b.ReportMetric(float64(adapter.resolved-resolves)/float64(iterations), "resolver-calls/op")
			b.ReportMetric(float64(replacements)/float64(iterations), "artifact-replacements/op")
		})
	}
}

func benchmarkIndexFiles(b *testing.B, root string) map[string]os.FileInfo {
	b.Helper()

	files, err := os.ReadDir(root)
	if err != nil {
		b.Fatal(err)
	}

	versions := make(map[string]os.FileInfo, len(files))
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		info, err := file.Info()
		if err != nil {
			b.Fatal(err)
		}

		versions[file.Name()] = info
	}

	return versions
}
