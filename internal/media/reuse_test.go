package media

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"duplex/internal/cache"
	"duplex/internal/source"
)

func TestYouTubeVerifiedIndexReuse(t *testing.T) {
	oldPath, newPath := os.Getenv("DUPLEX_REUSE_OLD_JSON"), os.Getenv("DUPLEX_REUSE_NEW_JSON")
	if oldPath == "" || newPath == "" {
		t.Skip("requires two private resolver responses for the same public source")
	}

	load := func(path string) source.Metadata {
		b, err := os.ReadFile(path) //nolint:gosec // G304: The opt-in qualification test reads the operator-selected private source fixture.
		if err != nil {
			t.Fatal(err)
		}

		var id struct {
			ID string `json:"id"`
		}
		if err = json.Unmarshal(b, &id); err != nil {
			t.Fatal(err)
		}

		m, err := source.DecodeMetadata(b, id.ID)
		if err != nil {
			t.Fatal(err)
		}

		return m
	}

	old, next := load(oldPath), load(newPath)
	if old.ID != next.ID {
		t.Fatal("reuse gate requires the same video")
	}

	find := func(m source.Metadata, id string) source.Format {
		for _, f := range m.Formats {
			if f.ID == id {
				return f
			}
		}

		t.Fatal("missing required format", id)

		return source.Format{}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()

	store, err := cache.New(filepath.Join(t.TempDir(), "chunks"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, store)

	client := &http.Client{Timeout: 30 * time.Second}
	open := func(m source.Metadata, format string) *HTTPSource {
		f := find(m, format)
		if f.Revision == "" {
			t.Fatal("provider version evidence missing")
		}

		s, err := OpenVersionedHTTP(ctx, client, f.URL, m.ID+":"+format, store, SourceVersion{Key: f.Revision, Size: f.VerifiedSize})
		if err != nil {
			t.Fatal(err)
		}

		return s
	}
	firstVideo, firstAudio := open(old, "401"), open(old, "140")
	started := time.Now()

	representation, err := Mux(ctx, firstVideo, firstAudio)
	if err != nil {
		t.Fatal(err)
	}

	cold := time.Since(started)

	path := filepath.Join(t.TempDir(), "index.json")
	if err = representation.SaveIndex(path); err != nil {
		t.Fatal(err)
	}

	started = time.Now()

	nextVideo, nextAudio := open(next, "401"), open(next, "140")
	if nextVideo.ID() != firstVideo.ID() || nextAudio.ID() != firstAudio.ID() {
		t.Fatal("fresh fingerprints or provider version differ across leases")
	}

	rebound, err := LoadIndex(path, map[string]Representation{nextVideo.ID(): nextVideo, nextAudio.ID(): nextAudio})
	if err != nil {
		t.Fatal(err)
	}

	warm := time.Since(started)

	if rebound.ID() != representation.ID() || rebound.Size() != representation.Size() {
		t.Fatal("rebound representation changed")
	}

	if err = VerifyRanges(ctx, rebound); err != nil {
		t.Fatal(err)
	}

	if warm >= cold {
		t.Fatal("index rebinding did not improve preparation time")
	}

	t.Logf("video=%s cold_index_seconds=%.3f verified_rebind_seconds=%.3f cold_requests=%d rebind_requests=%d rebind_bytes=%d source_bytes=%d", old.ID, cold.Seconds(), warm.Seconds(), firstVideo.Requests.Load()+firstAudio.Requests.Load(), nextVideo.Requests.Load()+nextAudio.Requests.Load(), nextVideo.UpstreamBytes.Load()+nextAudio.UpstreamBytes.Load(), nextVideo.Size()+nextAudio.Size())
}
