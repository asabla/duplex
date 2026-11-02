package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/config"
	"duplex/internal/source"
)

type thumbnailOnlySource struct {
	*collectionSource
	resolved atomic.Int64
	artwork  atomic.Int64
}

func (s *thumbnailOnlySource) Resolve(context.Context, string) (source.Metadata, error) {
	s.resolved.Add(1)
	return source.Metadata{}, errors.New("full resolution forbidden in thumbnail fixture")
}

func (s *thumbnailOnlySource) ArtworkURL(ctx context.Context, _ string) (string, error) {
	s.artwork.Add(1)
	return "https://i.ytimg.com/poster", ctx.Err()
}

type thumbnailTransport struct {
	requests    int
	picture     []byte
	etag        string
	conditional string
}

func (s *thumbnailTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.requests++
	s.conditional = r.Header.Get("If-None-Match")
	status := http.StatusOK

	data := s.picture
	if s.conditional == s.etag {
		status = http.StatusNotModified
		data = nil
	}

	return &http.Response{StatusCode: status, Header: http.Header{"Etag": {s.etag}}, Body: io.NopCloser(bytes.NewReader(data)), Request: r}, nil
}

func TestThumbnailWorkNeedsNoPlaybackAndRevalidatesChangedArtwork(t *testing.T) {
	adapter := &thumbnailOnlySource{collectionSource: fixtureCollection(1)}

	a := pipelineApp(t, pipelineChannel(config.DefaultSelection()), source.Observed{SourceAdapter: adapter})
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	a.scanPending = false

	var imageBytes bytes.Buffer

	picture := image.NewRGBA(image.Rect(0, 0, 16, 16))
	if err := png.Encode(&imageBytes, picture); err != nil {
		t.Fatal(err)
	}

	transport := &thumbnailTransport{picture: imageBytes.Bytes(), etag: `"first"`}
	previous := http.DefaultTransport
	http.DefaultTransport = transport

	t.Cleanup(func() { http.DefaultTransport = previous })

	if err := a.refreshAssets(t.Context(), "video000000"); err != nil {
		t.Fatal(err)
	}

	if adapter.resolved.Load() != 0 || adapter.artwork.Load() != 1 || transport.requests != 1 {
		t.Fatal("thumbnail work resolved playable formats", adapter.resolved.Load(), adapter.artwork.Load(), transport.requests)
	}

	first := a.loadAssets("video000000")[0]
	manifest := filepath.Join(a.original.DataDir, "assets", "video000000.json")

	before, err := os.Stat(manifest)
	if err != nil {
		t.Fatal(err)
	}

	if err = a.refreshAssets(t.Context(), "video000000"); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(manifest)
	if err != nil || !os.SameFile(before, after) || transport.requests != 1 {
		t.Fatal("unchanged artwork was downloaded or rewritten", err, transport.requests)
	}

	if a.scanPending {
		t.Fatal("artwork-only change requested a broad scan")
	}

	state := a.thumbnailState("video000000")
	state.Checked = time.Now().Add(-7 * time.Hour).Unix()
	writeThumbnailState(t, a, state)

	if err = a.refreshAssets(t.Context(), "video000000"); err != nil || transport.conditional != `"first"` || transport.requests != 2 {
		t.Fatal("unchanged artwork was not conditionally checked", err, transport.conditional)
	}

	state = a.thumbnailState("video000000")
	state.Checked = time.Now().Add(-7 * time.Hour).Unix()
	writeThumbnailState(t, a, state)
	picture.Set(2, 2, color.White)
	imageBytes.Reset()

	if err = png.Encode(&imageBytes, picture); err != nil {
		t.Fatal(err)
	}

	transport.picture, transport.etag = imageBytes.Bytes(), `"second"`

	if err = a.refreshAssets(t.Context(), "video000000"); err != nil {
		t.Fatal(err)
	}

	changed := a.loadAssets("video000000")[0]
	if changed.Media.ID() == first.Media.ID() || changed.Modified <= first.Modified || adapter.resolved.Load() != 0 {
		t.Fatal("genuine artwork change was lost or resolved formats")
	}

	if a.scanPending {
		t.Fatal("changed artwork requested a broad scan")
	}
}

func writeThumbnailState(t *testing.T, a *App, state thumbnailState) {
	t.Helper()

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}

	if err = config.AtomicWrite(a.thumbnailStatePath("video000000"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
