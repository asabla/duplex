package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestOfflineRepackMatchesFreshMuxAndRetainsOldBytes(t *testing.T) {
	if os.Getenv("DUPLEX_MEDIA_TEST") != "1" {
		t.Skip("requires controlled FFmpeg media fixtures")
	}

	dir := t.TempDir()
	videoPath, audioPath := fixture(t, dir)

	videoBytes, err := os.ReadFile(videoPath) //nolint:gosec // G304: This file belongs to the controlled fixture beneath t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	audioBytes, err := os.ReadFile(audioPath) //nolint:gosec // G304: This file belongs to the controlled fixture beneath t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	video, audio := indexTestSource{"video", videoBytes}, indexTestSource{"audio", audioBytes}

	current, err := Mux(t.Context(), video, audio)
	if err != nil {
		t.Fatal(err)
	}

	tracks := make([]muxTrack, 2)

	old := &VirtualMP4{Revision: legacyPackagingVersion, Sources: []string{video.ID(), audio.ID()}, Info: current.Info}
	for i, input := range []Representation{video, audio} {
		track, err := inspectMuxTrack(t.Context(), input, []string{"vide", "soun"}[i])
		if err != nil {
			t.Fatal(err)
		}

		spans, err := track.sourceSpans(i)
		if err != nil {
			t.Fatal(err)
		}

		old.Spans = append(old.Spans, spans...)
		track.useOutputOffsets(len(spans))
		track.track.Tkhd.TrackID = uint32(i + 1)
		tracks[i] = track
	}

	sort.SliceStable(old.Spans, func(i, j int) bool {
		a, b := old.Spans[i], old.Spans[j]
		return a.dts*uint64(b.scale) < b.dts*uint64(a.scale)
	})

	movie, err := muxMovie(tracks)
	if err != nil {
		t.Fatal(err)
	}

	if err = old.buildHeader(movie, tracks); err != nil {
		t.Fatal(err)
	}

	old.setIdentity()

	oldBytes := make([]byte, old.Size())
	if _, err = old.ReadAt(t.Context(), oldBytes, 0); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "legacy.json")
	if err = old.SaveIndex(path); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(path) //nolint:gosec // G304: This file belongs to the controlled fixture beneath t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	detached, err := LoadLayout(path)
	if err != nil {
		t.Fatal(err)
	}

	next, err := Repack(t.Context(), detached)
	if err != nil {
		t.Fatal(err)
	}

	if next.ID() != current.ID() || next.Size() != current.Size() || len(next.Spans) <= len(old.Spans) {
		t.Fatal("offline upgrade differs from fresh mux", next.ID(), current.ID(), len(next.Spans), len(old.Spans))
	}

	if err = next.bindSources(map[string]Representation{video.ID(): video, audio.ID(): audio}); err != nil {
		t.Fatal(err)
	}

	got, want := make([]byte, next.Size()), make([]byte, current.Size())
	if _, err = next.ReadAt(t.Context(), got, 0); err != nil {
		t.Fatal(err)
	}

	if _, err = current.ReadAt(t.Context(), want, 0); err != nil || !bytes.Equal(got, want) {
		t.Fatal("upgraded byte map differs", err)
	}

	afterOld := make([]byte, old.Size())
	if _, err = old.ReadAt(t.Context(), afterOld, 0); err != nil || !bytes.Equal(afterOld, oldBytes) {
		t.Fatal("old handle changed", err)
	}

	after, err := os.ReadFile(path) //nolint:gosec // G304: This file belongs to the controlled fixture beneath t.TempDir.
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("retained index changed", err)
	}

	if _, err = Repack(t.Context(), current); !errors.Is(err, ErrUnsupported) {
		t.Fatal("current version was upgraded", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err = Repack(ctx, detached); !errors.Is(err, context.Canceled) {
		t.Fatal("repack ignored cancellation", err)
	}

	// Malformed stored maps cannot cause payload reads or pointer replacement.
	saved, err := json.Marshal(detached)
	if err != nil {
		t.Fatal(err)
	}

	var malformed VirtualMP4
	if err = json.Unmarshal(saved, &malformed); err != nil {
		t.Fatal(err)
	}

	malformed.Spans[0].Output++
	if _, err = Repack(t.Context(), &malformed); err == nil {
		t.Fatal("malformed source offsets accepted")
	}
}
