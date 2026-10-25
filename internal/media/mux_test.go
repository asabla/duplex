package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"duplex/internal/cache"
)

func run(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s failed: %v: %s", name, err, stderr.String())
	}
	return b
}

func fixture(t *testing.T, dir string) (string, string) {
	t.Helper()
	v := filepath.Join(dir, "video.mp4")
	a := filepath.Join(dir, "audio.m4a")
	run(t, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=640x360:r=24", "-t", "30", "-an", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "18", "-movflags", "+faststart", v)
	run(t, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "30", "-vn", "-c:a", "aac", "-b:a", "128k", a)
	return v, a
}

func sourceServer(t *testing.T, paths ...string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, p := range paths {
		mux.HandleFunc("/"+filepath.Base(p), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"fixture-v1"`)
			http.ServeFile(w, r, p)
		})
	}
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestVirtualMP4MediaGate(t *testing.T) {
	testVirtualMP4(t, false)
}

func TestFragmentedMP4MediaGate(t *testing.T) {
	testVirtualMP4(t, true)
}

func testVirtualMP4(t *testing.T, fragmented bool) {
	if os.Getenv("DUPLEX_MEDIA_TEST") != "1" {
		t.Skip("set DUPLEX_MEDIA_TEST=1; requires ffmpeg and ffprobe")
	}
	dir := t.TempDir()
	vp, ap := fixture(t, dir)
	if fragmented {
		frag := func(input, output string) string {
			p := filepath.Join(dir, output)
			run(t, "ffmpeg", "-v", "error", "-i", input, "-c", "copy", "-movflags", "empty_moov+frag_keyframe+default_base_moof+global_sidx", "-frag_duration", "2000000", p)
			return p
		}
		vp = frag(vp, "video-frag.mp4")
		ap = frag(ap, "audio-frag.m4a")
	}
	server := sourceServer(t, vp, ap)
	store, err := cache.New(filepath.Join(dir, "cache"), 8*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	client := &http.Client{Timeout: 30 * time.Second}
	v, err := OpenHTTP(ctx, client, server.URL+"/"+filepath.Base(vp), "fixture-video", store)
	if err != nil {
		t.Fatal(err)
	}
	a, err := OpenHTTP(ctx, client, server.URL+"/"+filepath.Base(ap), "fixture-audio", store)
	if err != nil {
		t.Fatal(err)
	}
	virtual, err := Mux(ctx, v, a)
	if err != nil {
		t.Fatal(err)
	}
	indexBytes := v.UpstreamBytes.Load() + a.UpstreamBytes.Load()
	if indexBytes >= v.Size()/2 {
		t.Fatalf("index fetched too much media: %d / %d", indexBytes, v.Size())
	}
	// Read the end before the middle. The earlier payload is not a prerequisite.
	end := make([]byte, 65536)
	if _, err = virtual.ReadAt(ctx, end, virtual.Size()-int64(len(end))); err != nil {
		t.Fatal(err)
	}
	afterSeek := v.UpstreamBytes.Load() + a.UpstreamBytes.Load()
	if afterSeek-indexBytes > 3*ChunkSize {
		t.Fatalf("far seek fetched preceding content: %d", afterSeek-indexBytes)
	}
	output := filepath.Join(dir, "virtual.mp4")
	f, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(f, &Seeker{Context: ctx, Source: virtual})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(dir, "reference.mp4")
	run(t, "ffmpeg", "-v", "error", "-i", vp, "-i", ap, "-map", "0:v:0", "-map", "1:a:0", "-c", "copy", ref)
	type packet struct {
		Stream   int    `json:"stream_index"`
		PTS      string `json:"pts_time"`
		DTS      string `json:"dts_time"`
		Duration string `json:"duration_time"`
		Hash     string `json:"data_hash"`
	}
	packetSignature := func(path string) map[int][]packet {
		b := run(t, "ffprobe", "-v", "error", "-show_packets", "-show_data_hash", "sha256", "-show_entries", "packet=stream_index,pts_time,dts_time,duration_time,data_hash", "-of", "json", path)
		var data struct {
			Packets []packet `json:"packets"`
		}
		if err := json.Unmarshal(b, &data); err != nil {
			t.Fatal(err)
		}
		// Compare the requested packet fields. FFmpeg can also emit container-
		// specific side-data hints; decoded audio is checked independently below.
		tracks := map[int][]packet{}
		for _, p := range data.Packets {
			tracks[p.Stream] = append(tracks[p.Stream], p)
		}
		return tracks
	}
	if !reflect.DeepEqual(packetSignature(output), packetSignature(ref)) {
		t.Fatal("packet payloads or timestamps differ from independent FFmpeg mux")
	}
	// Compare the decoded audio across the full video presentation duration,
	// including its beginning and end, independent of container padding hints.
	pcm := func(path string) []byte {
		return run(t, "ffmpeg", "-v", "error", "-i", path, "-t", "30", "-map", "0:a:0", "-c:a", "pcm_s16le", "-f", "s16le", "-")
	}
	if !bytes.Equal(pcm(output), pcm(ref)) {
		t.Fatal("decoded audio differs from independent reference")
	}

	// Decode the whole virtual file and force timestamp-based seeks at both ends.
	run(t, "ffmpeg", "-v", "error", "-i", output, "-f", "null", "-")
	for _, seek := range []string{"1", "27"} {
		run(t, "ffmpeg", "-v", "error", "-ss", seek, "-i", output, "-t", "1", "-f", "null", "-")
	}
	gold, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for viewer := 0; viewer < 4; viewer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				offset := rand.Int64N(virtual.Size())
				size := min(int64(65536), virtual.Size()-offset)
				got := make([]byte, size)
				_, er := virtual.ReadAt(ctx, got, offset)
				if er != nil {
					t.Error(er)
					return
				}
				if !bytes.Equal(got, gold[offset:offset+size]) {
					t.Error("unstable random read")
					return
				}
			}
		}()
	}
	wg.Wait()
	if store.Stats().Allocated > store.Stats().Limit {
		t.Fatal("cache exceeded limit")
	}
	t.Logf("index_upstream_bytes=%d source_bytes=%d far_seek_upstream_bytes=%d output_bytes=%d allocated=%d", indexBytes, v.Size()+a.Size(), afterSeek-indexBytes, virtual.Size(), store.Stats().Allocated)
}

func TestRangeValidation(t *testing.T) {
	for _, mode := range []string{"ignored", "bad-range", "malformed-range", "changed-length", "changed-etag", "expired", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests == 1 {
					w.Header().Set("Content-Range", "bytes 0-0/100")
					w.Header().Set("ETag", `"v1"`)
					w.WriteHeader(206)
					w.Write([]byte{1})
					return
				}
				switch mode {
				case "ignored":
					w.WriteHeader(200)
				case "expired":
					w.WriteHeader(403)
				default:
					w.Header().Set("ETag", `"v1"`)
					w.Header().Set("Content-Range", "bytes 0-99/100")
					if mode == "bad-range" {
						w.Header().Set("Content-Range", "bytes 0-98/100")
					}
					if mode == "malformed-range" {
						w.Header().Set("Content-Range", "bytes 0-99/100 trailing")
					}
					if mode == "changed-length" {
						w.Header().Set("Content-Range", "bytes 0-99/101")
					}
					if mode == "changed-etag" {
						w.Header().Set("ETag", `"v2"`)
					}
					w.WriteHeader(206)
					if mode != "truncated" {
						w.Write(make([]byte, 100))
					}
				}
			}))
			defer s.Close()
			source, err := OpenHTTP(context.Background(), s.Client(), s.URL, "source", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = source.ReadAt(context.Background(), make([]byte, 8), 0)
			if err == nil {
				t.Fatal("accepted invalid range")
			}
		})
	}
}

func TestSeekerBounds(t *testing.T) {
	s := &Seeker{Source: memorySource{b: []byte("test")}}
	if _, err := s.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("accepted negative seek")
	}
	if _, err := s.Seek(0, 99); err == nil {
		t.Fatal("accepted invalid origin")
	}
}

func TestFragmentSyncWithUnspecifiedDependency(t *testing.T) {
	if !fragmentSync(0) || !fragmentSync(0x02000000) {
		t.Fatal("lost valid random-access point")
	}
	if fragmentSync(0x10000) || fragmentSync(0x02010000) || fragmentSync(0x01000000) {
		t.Fatal("dependent or non-sync sample treated as random access")
	}
}

type memorySource struct{ b []byte }

func (m memorySource) ID() string  { return fmt.Sprintf("memory-%d", len(m.b)) }
func (m memorySource) Size() int64 { return int64(len(m.b)) }
func (m memorySource) ReadAt(_ context.Context, p []byte, o int64) (int, error) {
	return bytes.NewReader(m.b).ReadAt(p, o)
}
