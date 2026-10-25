package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/cache"
	"duplex/internal/config"
	"duplex/internal/diagnostics"
	"duplex/internal/media"
	"duplex/internal/source"
)

type fixtureTransport struct {
	origin *url.URL
	base   http.RoundTripper
}

func (t fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	target := *request.URL
	target.Scheme, target.Host = t.origin.Scheme, t.origin.Host
	clone.URL = &target
	clone.Host = t.origin.Host

	return t.base.RoundTrip(clone)
}

type mediaFixtureOrigin struct {
	server     *httptest.Server
	files      map[string]string
	generation atomic.Int32
	expired    atomic.Bool
	requests   atomic.Int64
	bytes      atomic.Int64
}

func controlledMediaFiles(t testing.TB) map[string]string {
	t.Helper()

	if root := os.Getenv("DUPLEX_APP_MEDIA_FIXTURES"); root != "" {
		return map[string]string{"/video.mp4": filepath.Join(root, "video.mp4"), "/audio.m4a": filepath.Join(root, "audio.m4a")}
	}

	if os.Getenv("DUPLEX_MEDIA_TEST") != "1" {
		t.Skip("requires explicit controlled media tests")
	}

	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	video, audio := filepath.Join(root, "video.mp4"), filepath.Join(root, "audio.m4a")
	run := func(args ...string) {
		command := exec.CommandContext(t.Context(), binary, args...) //nolint:gosec // G204: Tests invoke the installed FFmpeg with fixed controlled fixture inputs.
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("controlled fixture generation: %v: %s", err, output)
		}
	}
	run("-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=25", "-t", "2", "-an", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-movflags", "+faststart", video)
	run("-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "2", "-vn", "-c:a", "aac", "-movflags", "+faststart", audio)

	return map[string]string{"/video.mp4": video, "/audio.m4a": audio}
}

func newMediaFixtureOrigin(t testing.TB) *mediaFixtureOrigin {
	t.Helper()
	origin := &mediaFixtureOrigin{files: controlledMediaFiles(t)}
	origin.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin.requests.Add(1)

		if origin.expired.Load() {
			http.Error(w, "expired fixture lease", http.StatusForbidden)
			return
		}

		file, ok := origin.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}

		handle, err := os.Open(file) //nolint:gosec // G304: The path is one of two generated fixtures beneath t.TempDir or the explicit Linux fixture directory.
		if err != nil {
			http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
			return
		}

		defer func() {
			if err := handle.Close(); err != nil {
				t.Error(err)
			}
		}()

		info, err := handle.Stat()
		if err != nil {
			t.Error(err)
			return
		}

		w.Header().Set("ETag", fmt.Sprintf(`"%s-generation-%d"`, r.URL.Path, origin.generation.Load()))
		http.ServeContent(&fixtureByteWriter{ResponseWriter: w, total: &origin.bytes}, r, r.URL.Path, info.ModTime(), handle)
	}))
	t.Cleanup(origin.server.Close)

	return origin
}

type fixtureByteWriter struct {
	http.ResponseWriter
	total *atomic.Int64
}

func (w *fixtureByteWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.total.Add(int64(n))

	return n, err
}

func mediaPipeline(t *testing.T) (*App, *collectionSource, *mediaFixtureOrigin) {
	t.Helper()
	origin := newMediaFixtureOrigin(t)
	adapter := fixtureCollection(1)
	metadata := adapter.metadata["video000000"]
	metadata.Duration = 2
	metadata.Timestamp = 1792886400
	expires := strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10)
	metadata.Formats = []source.Format{
		{ID: "137", URL: "https://r1.googlevideo.com/video.mp4?expire=" + expires, Protocol: "https", Ext: "mp4", VCodec: "avc1", ACodec: "none", Height: 1080},
		{ID: "140", URL: "https://r1.googlevideo.com/audio.m4a?expire=" + expires, Protocol: "https", Ext: "m4a", VCodec: "none", ACodec: "mp4a", Language: "en", Note: "Original audio"},
	}
	adapter.metadata[metadata.ID] = metadata
	app := pipelineApp(t, pipelineChannel(config.Selection{Mode: "latest", Limit: 1}), adapter)
	app.original = app.Config.Snapshot().Config

	app.Measurements = &diagnostics.Recorder{}
	if err := os.MkdirAll(app.original.IndexDir, 0o700); err != nil {
		t.Fatal(err)
	}

	chunks, err := cache.New(app.original.CacheDir, 16<<20)
	if err != nil {
		t.Fatal(err)
	}

	app.Cache = chunks

	t.Cleanup(func() {
		if err := chunks.Close(); err != nil {
			t.Error(err)
		}
	})

	parsed, err := url.Parse(origin.server.URL)
	if err != nil {
		t.Fatal(err)
	}

	app.httpClient = &http.Client{Transport: fixtureTransport{origin: parsed, base: http.DefaultTransport}, Timeout: 5 * time.Second}

	return app, adapter, origin
}

func TestMediaPreparationRebindAndChangedSourceKeepExactHandles(t *testing.T) {
	app, adapter, origin := mediaPipeline(t)
	if err := app.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	before, err := app.Catalog.Video(t.Context(), "video000000")
	if err != nil || before.Revision != "" || before.Preparation != "queued" {
		t.Fatal(before, err)
	}

	if err = app.prepareRecording(t.Context(), "video000000", false); err != nil {
		t.Fatal(err)
	}

	prepared, err := app.Catalog.Video(t.Context(), "video000000")
	if err != nil || prepared.Preparation != "ready" || prepared.Height != 1080 || prepared.Size < 1 {
		t.Fatal(prepared, err)
	}

	filename := fmt.Sprintf("Channel-%s/Season %04d/Channel-%s - S%04dE%04d.mp4", testOwner, prepared.Year, testOwner, prepared.Year, prepared.Number)
	provider := catalogueFS{app: app}

	old, _, err := provider.Open(t.Context(), filename)
	if err != nil {
		t.Fatal(err)
	}

	output := make([]byte, old.Size())
	if _, err = old.ReadAt(t.Context(), output, 0); err != nil {
		t.Fatal(err)
	}

	reference := sha256.Sum256(output)
	// Simulate unloaded runtime state while retaining durable catalogue and index.
	app.mu.Lock()
	app.reps = map[string]media.Representation{}
	app.metadata = map[string]metadataLease{}
	app.mu.Unlock()

	resolvedBefore := adapter.resolved

	var viewers sync.WaitGroup
	for range 4 {
		viewers.Add(1)
		go func() {
			defer viewers.Done()

			handle, _, err := provider.Open(t.Context(), filename)
			if err != nil {
				t.Error(err)
				return
			}

			data := make([]byte, handle.Size())
			if _, err = handle.ReadAt(t.Context(), data, 0); err != nil || sha256.Sum256(data) != reference {
				t.Error("rebound bytes differ", err)
			}
		}()
	}

	viewers.Wait()

	if adapter.resolved != resolvedBefore+1 {
		t.Fatal("simultaneous viewers did not share source binding", adapter.resolved, resolvedBefore)
	}

	after, err := app.Catalog.Video(t.Context(), "video000000")
	if err != nil || after.Revision != prepared.Revision || after.Updated != prepared.Updated {
		t.Fatal("same source changed durable revision or time", after, err)
	}

	// A new object version cannot be returned under an old open's identity.
	origin.generation.Add(1)
	app.mu.Lock()
	app.reps = map[string]media.Representation{}
	app.metadata = map[string]metadataLease{}
	app.mu.Unlock()

	if _, _, err = provider.Open(t.Context(), filename); !errors.Is(err, media.ErrChanged) {
		t.Fatal("changed source was silently rebound", err)
	}

	changed, err := app.Catalog.Video(t.Context(), "video000000")
	if err != nil || changed.Revision == prepared.Revision || changed.Preparation != "ready" {
		t.Fatal(changed, err)
	}

	oldBytes := make([]byte, len(output))
	if _, err = old.ReadAt(t.Context(), oldBytes, 0); err != nil || sha256.Sum256(oldBytes) != reference {
		t.Fatal("existing handle changed bytes", err)
	}

	if _, err = os.Stat(filepath.Join(app.original.IndexDir, changed.Revision+".description.json")); err != nil {
		t.Fatal(err)
	}

	origin.expired.Store(true)
	app.mu.Lock()
	app.reps = map[string]media.Representation{}
	app.metadata = map[string]metadataLease{}
	app.mu.Unlock()

	if _, _, err = provider.Open(t.Context(), filename); !errors.Is(err, media.ErrExpired) {
		t.Fatal("expired source became successful media", err)
	}

	failed, err := app.Catalog.Video(t.Context(), "video000000")
	if err != nil || failed.Revision != changed.Revision || failed.Preparation != "failed" {
		t.Fatal(failed, err)
	}

	t.Logf("fixture_requests=%d fixture_bytes=%d output_sha256=%s", origin.requests.Load(), origin.bytes.Load(), hex.EncodeToString(reference[:]))
}
