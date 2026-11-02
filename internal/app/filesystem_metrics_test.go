package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/config"
	"duplex/internal/diagnostics"
	"duplex/internal/plex"
	"duplex/internal/source"
)

type offlineMeasuredSource struct {
	*collectionSource
	requests atomic.Int64
}

func (s *offlineMeasuredSource) Resolve(_ context.Context, _ string) (source.Metadata, error) {
	s.requests.Add(1)
	return source.Metadata{}, errors.New("controlled source is offline")
}

func TestFirstVisitMetadataAndMissingAssetsNeverResolveMedia(t *testing.T) {
	adapter := &offlineMeasuredSource{collectionSource: fixtureCollection(0)}
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), adapter)
	a.Measurements = &diagnostics.Recorder{}
	prepared := projectionVideo(t, a, "video000001", 7)
	provider := catalogueFS{app: a}
	base := fmt.Sprintf("Channel-%s/Season 2026/Channel-%s - S2026E0007", testOwner, testOwner)
	p := config.Plex{URL: "http://plex.test:32400"}
	setPlaybackConfig(t, a, p)
	a.observePlayback(p, plex.Activity{Sessions: 1}, nil, time.Now())

	for range 2 {
		for _, name := range []string{base + ".nfo", "Channel-" + testOwner + "/tvshow.nfo"} {
			rep, _, err := provider.Open(t.Context(), name)
			if err != nil {
				t.Fatal(err)
			}

			data := make([]byte, rep.Size())
			if _, err = rep.ReadAt(t.Context(), data, 0); err != nil || len(data) == 0 {
				t.Fatal("metadata read failed while playback was protected", name, err)
			}
		}

		for _, suffix := range []string{".jpg", ".en.srt", ".bif", ".unknown"} {
			if _, err := provider.Stat(t.Context(), base+suffix); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("missing optional data did not return not-found", suffix, err)
			}
		}

		if _, err := provider.Stat(t.Context(), base+".mp4"); err != nil {
			t.Fatal(err)
		}
	}

	if adapter.requests.Load() != 0 {
		t.Fatal("episode metadata or optional assets resolved the source")
	}

	// A cold MP4 open has a separate measured source-binding boundary. Its
	// error remains explicit rather than turning absent data into success.
	if _, _, err := provider.Open(t.Context(), base+".mp4"); err == nil || adapter.requests.Load() != 1 {
		t.Fatal("uncached media did not expose the source-binding failure", err)
	}

	a.cacheRepresentation(mountedBytes{id: prepared.Revision, data: []byte("immutable bytes!")}, time.Now().Add(time.Hour), prepared.Metadata.ID)

	rep, err := provider.OpenVersion(t.Context(), base+".mp4", prepared.Revision)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = rep.ReadAt(t.Context(), make([]byte, 4), 0); err != nil || adapter.requests.Load() != 1 {
		t.Fatal("warm protected playback fetched another source", err)
	}

	seen := map[string]bool{}
	for _, item := range a.Measurements.Snapshot() {
		seen[item.Operation] = true
		if strings.Contains(item.Operation, testOwner) || strings.Contains(item.Operation, prepared.Metadata.ID) {
			t.Fatal("filesystem measurements exposed identities")
		}
	}

	for _, operation := range []string{"filesystem.stat.metadata", "filesystem.stat.media", "filesystem.open.metadata", "filesystem.read.metadata", "filesystem.open.media", "filesystem.bind.media", "filesystem.read.media", "playback.binding", "playback.metadata"} {
		if !seen[operation] {
			t.Fatal("missing first-visit measurement", operation)
		}
	}
}
