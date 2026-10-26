package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"testing"
	"time"

	"duplex/internal/config"
	"duplex/internal/source"
)

func initialAssetFixture(t *testing.T) (*App, source.Metadata, *scanAssetTransport) {
	t.Helper()

	adapter := fixtureCollection(1)
	m := adapter.metadata["video000000"]
	m.Thumbnail = "https://i.ytimg.com/poster"
	m.Formats = []source.Format{{ID: "combined", URL: "https://r1.googlevideo.com/media", Protocol: "https", Ext: "mp4", VCodec: "avc1", ACodec: "mp4a", Height: 1080, Language: "en"}}
	m.Subtitles = map[string][]source.Subtitle{"en": {{Ext: "srt", URL: "https://www.youtube.com/captions"}}}
	adapter.metadata[m.ID] = m

	a := pipelineApp(t, pipelineChannel(config.DefaultSelection()), adapter)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 128, 72))); err != nil {
		t.Fatal(err)
	}

	transport := &scanAssetTransport{base: assetFixtureTransport{
		"/poster": {data: picture.Bytes()}, "/captions": {data: []byte("original captions")},
	}}
	previous := http.DefaultTransport
	http.DefaultTransport = transport

	t.Cleanup(func() { http.DefaultTransport = previous })

	return a, m, transport
}

func TestInitialAssetsReadyWithoutDuplicateBackgroundWork(t *testing.T) {
	a, m, transport := initialAssetFixture(t)
	if err := a.queueRecordingAssets(t.Context(), m, true); err != nil {
		t.Fatal(err)
	}

	if entries := a.loadAssets(m.ID); len(entries) != 2 || transport.requests.Load() != 2 {
		t.Fatal("initial artifacts were not prepared", entries, transport.requests.Load())
	}

	var state string
	if err := a.Catalog.DB.QueryRowContext(t.Context(), `SELECT state FROM asset_jobs WHERE video_id=?`, m.ID).Scan(&state); err != nil || state != "ready" {
		t.Fatal(state, err)
	}

	if err := a.queueRecordingAssets(t.Context(), m, true); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Catalog.NextAsset(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("completed initial work was queued twice", err)
	}

	if transport.requests.Load() != 2 {
		t.Fatal("completed initial work was fetched twice")
	}
}

func TestInitialOptionalFailuresKeepMediaReady(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			a, m, transport := initialAssetFixture(t)
			transport.base["/poster"] = assetFixtureResponse{status: http.StatusServiceUnavailable}
			transport.base["/captions"] = assetFixtureResponse{status: http.StatusServiceUnavailable}

			if !supported {
				m.Formats = nil
			}

			if err := a.queueRecordingAssets(t.Context(), m, true); err != nil {
				t.Fatal("optional failure escaped into media preparation", err)
			}

			var state, message string
			if err := a.Catalog.DB.QueryRowContext(t.Context(), `SELECT state,error FROM asset_jobs WHERE video_id=?`, m.ID).Scan(&state, &message); err != nil || state != "failed" || message == "" {
				t.Fatal("optional failure was not visible", state, message, err)
			}

			entries := a.loadAssets(m.ID)
			if len(entries) != 1 || entries[0].Path != ".jpg" {
				t.Fatal("optional failure lost fallback", entries)
			}

			if err := a.ensureInitialThumbnail(m); err != nil || a.loadAssets(m.ID)[0].Media.ID() != entries[0].Media.ID() {
				t.Fatal("existing fallback was replaced", err)
			}
		})
	}
}

func TestInitialAssetDeadlineRetainsFallbackAndWakesRetry(t *testing.T) {
	a, m, transport := initialAssetFixture(t)
	transport.delay = time.Second
	started := time.Now()

	if err := a.queueRecordingAssets(t.Context(), m, true); err != nil {
		t.Fatal("optional timeout failed media preparation", err)
	}

	if time.Since(started) >= time.Second {
		t.Fatal("optional work did not obey the initial deadline")
	}

	entries := a.loadAssets(m.ID)
	if len(entries) != 1 || entries[0].Path != ".jpg" {
		t.Fatal("deadline lost durable fallback", entries)
	}

	id, err := a.Catalog.NextAsset(t.Context())
	if err != nil || id != m.ID {
		t.Fatal("interrupted work was not queued", id, err)
	}

	wake, _ := a.assetChannels()
	select {
	case <-wake:
	default:
		t.Fatal("retry did not wake the worker")
	}

	transport.delay = 0

	if err := a.runAssetWork(t.Context(), id); err != nil {
		t.Fatal(err)
	}

	if entries := a.loadAssets(m.ID); len(entries) != 2 {
		t.Fatal("deferred work did not replace fallback and add captions", entries)
	}
}

func TestInitialAssetsCancellationAndSerializedWork(t *testing.T) {
	a, m, transport := initialAssetFixture(t)

	release, err := a.acquireAsset(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := a.acquireAsset(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("asset capacity wait ignored cancellation", err)
	}

	// The occupied asset slot consumes the same deadline as network work.
	if err := a.queueRecordingAssets(t.Context(), m, true); err != nil {
		t.Fatal(err)
	}

	if transport.requests.Load() != 0 || len(a.loadAssets(m.ID)) != 1 {
		t.Fatal("optional work exceeded its single active operation bound")
	}

	if _, err := a.Catalog.NextAsset(t.Context()); err != nil {
		t.Fatal("slot timeout was not retained for retry", err)
	}
}

func TestPreparedMediaOpensWhileInitialAssetsWait(t *testing.T) {
	a, adapter, _ := mediaPipeline(t)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	release, err := a.acquireAsset(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	done := make(chan error, 1)
	go func() { done <- a.prepareRecording(t.Context(), "video000000", false) }()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	for {
		ready, err := a.Catalog.PreparedForHeight(ctx, "video000000", a.Config.Snapshot().Config.MaxHeight)
		if err != nil {
			t.Fatal(err)
		}

		if ready {
			break
		}

		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}

	video, err := a.Catalog.Video(ctx, "video000000")
	if err != nil {
		t.Fatal(err)
	}

	name := fmt.Sprintf("Channel-%s/Season %04d/Channel-%s - S%04dE%04d.mp4", testOwner, video.Year, testOwner, video.Year, video.Number)
	provider := catalogueFS{app: a}

	rep, _, err := provider.Open(ctx, name)
	if err != nil || rep.Size() != video.Size {
		t.Fatal("optional asset wait blocked cached playback", err)
	}

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if adapter.resolved > 2 {
		t.Fatal("optional asset wait forced a source rebind")
	}
}

func TestAssetWorkerDrainsQueuedRecordingsWithoutPollingPerItem(t *testing.T) {
	a, m, transport := initialAssetFixture(t)

	adapter, ok := a.Source.(*collectionSource)
	if !ok {
		t.Fatal("unexpected fixture adapter")
	}

	second := m
	second.ID = "video000001"
	adapter.metadata[second.ID] = second

	if err := a.Catalog.SaveMetadata(t.Context(), second); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{m.ID, second.ID} {
		if err := a.Catalog.QueueAsset(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	a.wg.Add(1)
	go a.assetWorker(ctx)

	t.Cleanup(func() { cancel(); a.wg.Wait() })

	for {
		var ready int
		if err := a.Catalog.DB.QueryRowContext(ctx, `SELECT count(*) FROM asset_jobs WHERE state='ready'`).Scan(&ready); err != nil {
			t.Fatal("asset queue still waited for a polling tick", err)
		}

		if ready == 2 {
			break
		}

		select {
		case <-ctx.Done():
			t.Fatal("asset queue still waited for a polling tick")
		case <-time.After(time.Millisecond):
		}
	}

	if transport.requests.Load() != 4 {
		t.Fatal("asset requests duplicated", transport.requests.Load())
	}
}
