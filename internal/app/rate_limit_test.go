package app

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/media"
)

type rateLimitedTransport struct {
	base     http.RoundTripper
	limited  atomic.Bool
	rejected atomic.Int64
}

func TestRateLimitedPreparationWaitsAndRetainsPreparedMedia(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprint(retained), func(t *testing.T) {
			a, _, _ := mediaPipeline(t)
			transport := &rateLimitedTransport{base: a.httpClient.Transport}
			a.httpClient.Transport = transport

			if err := a.Sync(t.Context(), testOwner); err != nil {
				t.Fatal(err)
			}

			if retained {
				if err := a.prepareRecording(t.Context(), "video000000", false); err != nil {
					t.Fatal(err)
				}
			}

			before, err := a.Catalog.Video(t.Context(), "video000000")
			if err != nil {
				t.Fatal(err)
			}

			transport.limited.Store(true)
			a.runPreparation(t.Context(), catalog.PreparationTask{VideoID: before.ID, Priority: renewalPriority})

			after, err := a.Catalog.Video(t.Context(), before.ID)
			if err != nil || after.Revision != before.Revision || after.Size != before.Size {
				t.Fatal("deferred preparation changed retained media", after, err)
			}

			want := "waiting"
			if retained {
				want = "ready"
			}

			if after.Preparation != want || transport.rejected.Load() != 1 {
				t.Fatal("temporary limit was treated as a failure", after.Preparation, transport.rejected.Load())
			}
		})
	}
}

func (r *rateLimitedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !r.limited.Load() {
		return r.base.RoundTrip(request)
	}

	r.rejected.Add(1)

	return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(strings.NewReader("rate limited")), Request: request}, nil
}

func TestRateLimitedProbesKeepPreparedRecordingReady(t *testing.T) {
	a, adapter, _ := mediaPipeline(t)
	transport := &rateLimitedTransport{base: a.httpClient.Transport}
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

	if err = a.Cache.Resize(4096); err != nil {
		t.Fatal(err)
	}

	if err = a.Cache.Resize(16 << 20); err != nil {
		t.Fatal(err)
	}

	a.mu.Lock()
	delete(a.reps, video.Revision)
	delete(a.leases, video.Revision)
	delete(a.metadata, video.ID)
	a.mu.Unlock()
	adapter.mu.Lock()
	before := adapter.resolved
	adapter.mu.Unlock()
	transport.limited.Store(true)

	name := fmt.Sprintf("Channel-%s/Season %04d/Channel-%s - S%04dE%04d.mp4", testOwner, video.Year, testOwner, video.Year, video.Number)

	for range 5 {
		handle, _, err := (catalogueFS{app: a}).Open(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}

		if n, err := handle.ReadAt(t.Context(), make([]byte, 16), 0); err != nil || n != 16 {
			t.Fatal("local header was blocked", n, err)
		}

		if _, err := handle.ReadAt(t.Context(), make([]byte, 16), int64(len(index.Header))); !errors.Is(err, media.ErrDeferred) {
			t.Fatal("rate-limited probe was not deferred", err)
		}
	}

	after, err := a.Catalog.Video(t.Context(), video.ID)
	if err != nil || after.Preparation != "ready" || after.Revision != video.Revision || after.Size != video.Size {
		t.Fatal("temporary rate limit failed or changed prepared media", after, err)
	}

	adapter.mu.Lock()
	defer adapter.mu.Unlock()

	if transport.rejected.Load() != 1 || adapter.resolved != before+1 {
		t.Fatal("repeated probes bypassed their shared delay", transport.rejected.Load(), adapter.resolved-before)
	}
}
