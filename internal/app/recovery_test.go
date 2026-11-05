package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/media"
)

type revokedTransport struct {
	base    http.RoundTripper
	revoked atomic.Bool
	rejects atomic.Int64
}

func (t *revokedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.revoked.Load() && r.URL.Query().Get("lease") != "fresh" {
		t.rejects.Add(1)
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("expired")), Request: r}, nil
	}

	return t.base.RoundTrip(r)
}

func TestRejectedPayloadLeaseRefreshesOnceAndRetainsIdentity(t *testing.T) {
	a, adapter, _ := mediaPipeline(t)
	transport := &revokedTransport{base: a.httpClient.Transport}

	a.httpClient.Transport = transport
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

	name := fmt.Sprintf("Channel-%s/Season %04d/Channel-%s - S%04dE%04d.mp4", testOwner, video.Year, testOwner, video.Year, video.Number)

	handle, _, err := (catalogueFS{app: a}).Open(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}

	// Retain a handle that already contains the original binding.
	if _, err = handle.ReadAt(t.Context(), make([]byte, 16), 0); err != nil {
		t.Fatal(err)
	}

	if err = a.Cache.Resize(4096); err != nil {
		t.Fatal(err)
	}

	if err = a.Cache.Resize(16 << 20); err != nil {
		t.Fatal(err)
	}

	adapter.mu.Lock()

	metadata := adapter.metadata[video.ID]
	for i := range metadata.Formats {
		metadata.Formats[i].URL += "&lease=fresh"
	}

	adapter.metadata[video.ID] = metadata
	before := adapter.resolved
	adapter.mu.Unlock()
	transport.revoked.Store(true)

	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()

			if n, err := handle.ReadAt(t.Context(), make([]byte, 32), int64(len(index.Header))); err != nil || n != 32 {
				t.Error(n, err)
			}
		}()
	}

	readers.Wait()
	adapter.mu.Lock()
	resolved := adapter.resolved - before
	adapter.mu.Unlock()

	if resolved != 1 || transport.rejects.Load() == 0 {
		t.Fatal("recovery did not share a fresh resolution", resolved, transport.rejects.Load())
	}

	after, err := a.Catalog.Video(t.Context(), video.ID)
	if err != nil || after.Revision != video.Revision || after.Size != video.Size || after.Updated != video.Updated {
		t.Fatal("renewal changed durable media", after, err)
	}

	bound := a.cachedBinding(video.Revision)
	a.rejectBinding(catalog.Episode{VideoID: video.ID, Revision: video.Revision}, bound.epoch-1)

	if a.cachedRepresentation(video.Revision) == nil {
		t.Fatal("late failure invalidated fresh binding")
	}
}

func BenchmarkWarmPayloadRange(b *testing.B) {
	a, adapter, origin := mediaPipeline(b)
	if err := a.Sync(b.Context(), testOwner); err != nil {
		b.Fatal(err)
	}

	if err := a.prepareRecording(b.Context(), "video000000", false); err != nil {
		b.Fatal(err)
	}

	v, err := a.Catalog.Video(b.Context(), "video000000")
	if err != nil {
		b.Fatal(err)
	}

	index, ok := a.reps[v.Revision].(*media.VirtualMP4)
	if !ok {
		b.Fatal("fixture did not prepare a virtual MP4")
	}

	saved, err := media.LoadLayout(filepath.Join(a.original.IndexDir, v.Revision+".mp4-index.json"))
	if err != nil {
		b.Fatal(err)
	}

	span := index.Spans[0]
	// Warm the exact source chunk used by this benchmark, outside measured work.
	scratch := make([]byte, 4)
	if _, err = index.ReadSource(b.Context(), span.SourceID, scratch, span.Offset); err != nil {
		b.Fatal(err)
	}

	reader := &lazyTrack{app: a, id: span.SourceID, size: saved.SourceLengths[span.SourceID], episode: catalog.Episode{VideoID: v.ID, Revision: v.Revision, Size: v.Size}}

	data := make([]byte, 4)
	if _, err = reader.ReadAt(context.Background(), data, index.Spans[0].Offset); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.SetBytes(4)

	requests := origin.requests.Load()

	adapter.mu.Lock()
	resolved := adapter.resolved
	adapter.mu.Unlock()

	for b.Loop() {
		if _, err = reader.ReadAt(b.Context(), data, index.Spans[0].Offset); err != nil {
			b.Fatal(err)
		}
	}

	adapter.mu.Lock()
	defer adapter.mu.Unlock()

	if origin.requests.Load() != requests || adapter.resolved != resolved {
		b.Fatal("warm payload reads performed upstream work")
	}
}

func TestWarmPayloadReadMemoryBudget(t *testing.T) {
	if os.Getenv("DUPLEX_MEDIA_TEST") != "1" && os.Getenv("DUPLEX_APP_MEDIA_FIXTURES") == "" {
		t.Skip("requires explicit controlled media tests")
	}

	result := testing.Benchmark(BenchmarkWarmPayloadRange)
	if result.N == 0 || result.AllocedBytesPerOp() > 4096 || result.AllocsPerOp() > 32 {
		t.Fatal("four-byte warm payload read exceeded its memory budget", result.MemString())
	}
}
