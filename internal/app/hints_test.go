package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/media"
	"duplex/internal/source"
)

func hintFixture(t *testing.T) (*App, *collectionSource) {
	t.Helper()
	adapter := fixtureCollection(0)
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), adapter)
	for i := range 3 {
		projectionVideo(t, a, fmt.Sprintf("video%06d", i), i+1)
	}
	adapter.lists = []source.Playlist{{ID: "PLfixture0001", Title: "Ordered"}}
	adapter.entries["PLfixture0001"] = []source.Entry{{ID: "video000000"}, {ID: "video000001"}, {ID: "video000002"}}
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	return a, adapter
}

func hintBytes(t *testing.T, rep media.Representation) []byte {
	t.Helper()
	data := make([]byte, rep.Size())
	if _, err := rep.ReadAt(t.Context(), data, 0); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestHintsAreStableAcrossRestartAndImmutableAcrossReorder(t *testing.T) {
	a, adapter := hintFixture(t)
	provider := catalogueFS{app: a}
	name := fmt.Sprintf("Channel-%s/Season 0001/.plexmatch", testOwner)
	old, modified, err := provider.Open(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	initial := hintBytes(t, old)
	if !strings.Contains(string(initial), fmt.Sprintf("ep: S01E01: Channel-%s - S0001E0001.mp4", testOwner)) {
		t.Fatal("initial file hint is wrong")
	}
	// Reopening an index after process cache loss must verify and retain it.
	a.mu.Lock()
	a.hints = nil
	a.mu.Unlock()
	reopened, reopenedAt, err := provider.Open(t.Context(), name)
	if err != nil || reopened.ID() != old.ID() || reopenedAt != modified {
		t.Fatal("verified hints were not reused", err)
	}
	adapter.entries["PLfixture0001"] = []source.Entry{{ID: "video000002"}, {ID: "video000000"}, {ID: "video000001"}}
	if err = a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	current, currentAt, err := provider.Open(t.Context(), name)
	if err != nil || current.ID() == old.ID() || currentAt <= modified {
		t.Fatal("changed order retained stale hint identity or time", err)
	}
	if !strings.Contains(string(hintBytes(t, current)), fmt.Sprintf("ep: S01E01: Channel-%s - S0001E0003.mp4", testOwner)) {
		t.Fatal("reordered hints changed the media slot instead of its match index")
	}
	if !bytes.Equal(initial, hintBytes(t, old)) {
		t.Fatal("already open hint representation changed bytes")
	}
	// Paging must include the dotfile once, even with one entry per page.
	after := ""
	seen := map[string]bool{}
	for range 10 {
		page, err := provider.ReadDir(t.Context(), filepath.Dir(name), after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if seen[page[0].Name] {
			t.Fatal("directory page repeated", page[0].Name)
		}
		seen[page[0].Name] = true
		after = page[0].Name
	}
	if len(seen) != 7 || !seen[".plexmatch"] {
		t.Fatal("hint paging lost files", len(seen))
	}
}

func TestHintManifestAndBlobCorruptionAreRebuiltFromCatalogue(t *testing.T) {
	a, _ := hintFixture(t)
	before, err := a.seasonHints(t.Context(), testOwner, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := hintBytes(t, before.Media)
	asset, ok := before.Media.(localAsset)
	if !ok {
		t.Fatal("hint artifact is not a durable local file")
	}
	if err = os.WriteFile(asset.file, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.hints = nil
	a.mu.Unlock()
	current, err := a.seasonHints(t.Context(), testOwner, 1)
	if err != nil || !bytes.Equal(want, hintBytes(t, current.Media)) {
		t.Fatal("corrupt hint bytes were trusted", err)
	}
	if err = os.WriteFile(a.hintManifest(testOwner, 1), bytes.Repeat([]byte("a"), 4097), 0o600); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.hints = nil
	a.mu.Unlock()
	current, err = a.seasonHints(t.Context(), testOwner, 1)
	if err != nil || !bytes.Equal(want, hintBytes(t, current.Media)) {
		t.Fatal("invalid manifest blocked verified reconstruction", err)
	}
	entries, err := os.ReadDir(a.hintDirectory())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".hints-") {
			t.Fatal("temporary hint write was not removed")
		}
	}
}

type failedHintWriter struct{}

func (failedHintWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestHintsValidateReferencesAndPropagateFailures(t *testing.T) {
	for _, ref := range []string{"../outside.plexmatch", strings.Repeat("g", 64) + ".plexmatch", strings.Repeat("a", 64) + ".json", ""} {
		if validHintReference(ref) {
			t.Fatal("unsafe hint reference accepted")
		}
	}
	if err := writeHints(failedHintWriter{}, testOwner, 1, []catalog.FileHint{{Number: 1, FileNumber: 1}}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("hint write failure was hidden", err)
	}
	if err := writeHints(io.Discard, testOwner, 1, []catalog.FileHint{{Number: 0, FileNumber: 1}}); err == nil {
		t.Fatal("invalid scanner slot accepted")
	}
	a, _ := hintFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := a.seasonHints(ctx, testOwner, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("hint generation ignored cancellation", err)
	}
	if _, err := a.seasonHints(t.Context(), testOwner, 2026); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("year season acquired playlist hints", err)
	}
	provider := catalogueFS{app: a}
	if _, err := provider.Stat(t.Context(), "Channel-"+testOwner+"/Season 2026/.plexmatch"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("legacy year season gained file hints", err)
	}
}
