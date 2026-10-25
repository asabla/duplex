package media

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"duplex/internal/cache"
)

// TestYouTube4KGate consumes a fresh resolver result from an explicit public
// source. Signed locators remain in temporary storage and never enter evidence.
func TestYouTube4KGate(t *testing.T) {
	testRecordedSource(t, os.Getenv("DUPLEX_YOUTUBE_JSON"), "401", 2160, false)
}

func TestYouTubeReplayGate(t *testing.T) {
	testRecordedSource(t, os.Getenv("DUPLEX_REPLAY_JSON"), "137", 1080, true)
}

func TestYouTube4KReplayGate(t *testing.T) {
	testRecordedSource(t, os.Getenv("DUPLEX_REPLAY_JSON"), "401", 2160, true)
}

func TestYouTubeCombinedReplayGate(t *testing.T) {
	testRecordedSource(t, os.Getenv("DUPLEX_REPLAY_JSON"), "18", 360, true)
}

func testRecordedSource(t *testing.T, path, videoFormat string, height int, replay bool) {
	if path == "" {
		t.Skip("set the gate's resolver JSON environment variable to opt in to real-source testing")
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: The opt-in qualification test reads the operator-selected private source fixture.
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		ID         string `json:"id"`
		Duration   int    `json:"duration"`
		LiveStatus string `json:"live_status"`
		Formats    []struct {
			ID  string `json:"format_id"`
			URL string `json:"url"`
		} `json:"formats"`
	}
	if err = json.Unmarshal(b, &info); err != nil {
		t.Fatal("invalid source metadata")
	}
	if info.Duration < 60 || (info.LiveStatus != "not_live" && info.LiveStatus != "was_live") {
		t.Fatal("the gate requires a finite recorded source")
	}
	if replay && info.LiveStatus != "was_live" {
		t.Fatal("the replay gate requires a completed broadcast")
	}
	urls := map[string]string{}
	for _, f := range info.Formats {
		urls[f.ID] = f.URL
	}

	combined := videoFormat == "18"
	if urls[videoFormat] == "" || (!combined && urls["140"] == "") {
		t.Fatal("test source lacks the required recorded-media formats")
	}
	budget := 90 * time.Second
	if raw := os.Getenv("DUPLEX_SOURCE_TEST_TIMEOUT"); raw != "" {
		var e error
		budget, e = time.ParseDuration(raw)
		if e != nil || budget < time.Second || budget > 30*time.Minute {
			t.Fatal("invalid source test timeout")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	store, err := cache.New(filepath.Join(t.TempDir(), "cache"), 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, store)
	client := &http.Client{Timeout: 30 * time.Second}
	if os.Getenv("DUPLEX_SOURCE_HTTP1") == "1" {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			t.Fatal("default HTTP transport has changed")
		}
		transport := base.Clone()
		transport.Protocols = new(http.Protocols)
		transport.Protocols.SetHTTP1(true)
		client.Transport = transport
		defer transport.CloseIdleConnections()
	}
	v, err := OpenHTTP(ctx, client, urls[videoFormat], info.ID+":"+videoFormat, store)
	if err != nil {
		t.Fatal("video probe:", err)
	}
	sources := []*HTTPSource{v}
	var virtual Representation = v
	started := time.Now()
	if !combined {
		var a *HTTPSource
		a, err = OpenHTTP(ctx, client, urls["140"], info.ID+":140", store)
		if err != nil {
			t.Fatal("audio probe:", err)
		}
		sources = append(sources, a)
		virtual, err = Mux(ctx, v, a)
	}
	t.Logf("index_seconds=%.3f", time.Since(started).Seconds())
	stats := func() (size, fetched, requests int64) {
		for _, source := range sources {
			size += source.Size()
			fetched += source.UpstreamBytes.Load()
			requests += source.Requests.Load()
		}
		return
	}
	defer func() {
		_, fetched, requests := stats()
		t.Logf("source bytes fetched=%d requests=%d", fetched, requests)
	}()
	if err != nil {
		t.Fatal("recorded mux:", err)
	}
	for _, offset := range []int64{0, virtual.Size() - 65536, virtual.Size() / 2, virtual.Size() / 4} {
		one, two := make([]byte, 65536), make([]byte, 65536)
		if _, err = virtual.ReadAt(ctx, one, offset); err != nil {
			t.Fatal(err)
		}
		if _, err = virtual.ReadAt(ctx, two, offset); err != nil {
			t.Fatal(err)
		}
		for n := range one {
			if one[n] != two[n] {
				t.Fatal("unstable remote read")
			}
		}
	}
	_, fetched, _ := stats()
	t.Logf("range checks upstream=%d", fetched)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "media.mp4", time.Time{}, &Seeker{Context: r.Context(), Source: virtual})
	}))
	defer server.Close()
	probe := run(t, "ffprobe", "-v", "error", "-show_entries", "stream=codec_type,width,height", "-of", "json", server.URL)
	_, fetched, _ = stats()
	t.Logf("stream probe upstream=%d", fetched)
	var streams struct {
		Streams []struct {
			CodecType     string `json:"codec_type"`
			Width, Height int
		} `json:"streams"`
	}
	if err = json.Unmarshal(probe, &streams); err != nil {
		t.Fatal(err)
	}
	verified := false
	for _, stream := range streams.Streams {
		if stream.CodecType == "video" && stream.Height == height {
			verified = true
		}
	}
	if !verified {
		t.Fatal("virtual output does not match the test quality profile")
	}
	for _, seek := range []string{"1", fmt.Sprint(info.Duration - 55)} {
		started = time.Now()
		run(t, "ffmpeg", "-v", "error", "-threads", "2", "-ss", seek, "-i", server.URL, "-t", "0.25", "-threads", "2", "-f", "null", "-")
		_, fetched, _ = stats()
		t.Logf("decode seek=%s upstream=%d seconds=%.3f", seek, fetched, time.Since(started).Seconds())
	}
	limit := int64(32 * 1024 * 1024)
	if info.Duration > 3600 {
		limit = 128 * 1024 * 1024
	}
	size, got, _ := stats()
	if got > limit {
		t.Fatalf("unexpected remote traffic: %d", got)
	}
	t.Logf("video_id=%s video_format=%s combined=%t height=%d live_status=%s source_bytes=%d output_bytes=%d upstream_bytes=%d", info.ID, videoFormat, combined, height, info.LiveStatus, size, virtual.Size(), got)
}
