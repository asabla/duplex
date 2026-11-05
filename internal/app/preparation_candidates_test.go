package app

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"duplex/internal/source"
)

type rejectedCombinedTransport struct {
	base    http.RoundTripper
	rejects atomic.Int64
}

func (transport *rejectedCombinedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Query().Get("bad") == "1" {
		transport.rejects.Add(1)
		return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("rejected format")), Request: request}, nil
	}

	return transport.base.RoundTrip(request)
}

func TestPreparationCandidateBudgetRetainsSeparateFallback(t *testing.T) {
	for _, rejected := range []int{5, 6, 40} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			a, adapter, _ := mediaPipeline(t)
			transport := &rejectedCombinedTransport{base: a.httpClient.Transport}
			a.httpClient.Transport = transport

			metadata := adapter.metadata["video000000"]
			for i := range rejected {
				metadata.Formats = append(metadata.Formats, source.Format{ID: fmt.Sprintf("combined%d", i), URL: metadata.Formats[0].URL + "&bad=1", Protocol: "https", Ext: "mp4", VCodec: "avc1", ACodec: "mp4a", Height: 2160})
			}

			prepared, err := a.Prepare(t.Context(), metadata, 2160)
			if err != nil || prepared.Height != 1080 {
				t.Fatal("separate fallback failed", prepared.Height, err)
			}

			if got := transport.rejects.Load(); got != int64(min(rejected, 6)) {
				t.Fatal("combined request budget exceeded or fallback started too early", got)
			}
		})
	}
}

func TestPreparationCandidateBudgetBoundsEachKind(t *testing.T) {
	for _, combined := range []bool{false, true} {
		t.Run(fmt.Sprint(combined), func(t *testing.T) {
			metadata := source.Metadata{Formats: []source.Format{{ID: "audio", URL: "https://r1.googlevideo.com/audio", Protocol: "https", Ext: "m4a", VCodec: "none", ACodec: "mp4a"}}}

			for i := range 40 {
				codec := "none"
				if combined {
					codec = "mp4a"
				}

				metadata.Formats = append(metadata.Formats, source.Format{ID: fmt.Sprint(i), URL: "https://r1.googlevideo.com/video", Protocol: "https", Ext: "mp4", VCodec: "avc1", ACodec: codec, Height: 1080})
			}

			plan := preparationCandidates(metadata, 2160)
			if len(plan) != 6 || plan[0].Video.ID != "0" || plan[5].Video.ID != "5" {
				t.Fatal("plan did not retain the first six ranked candidates", plan)
			}
		})
	}
}
