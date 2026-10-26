package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"duplex/internal/config"
	"duplex/internal/library"
)

func TestAssetManifestTracksContentChangesWithinOneSecond(t *testing.T) {
	a := pipelineApp(t, pipelineChannel(config.DefaultSelection()), fixtureCollection(1))

	const id = "video000000"

	stamp := time.Now().Add(-time.Hour)

	var old library.Entry

	for i, content := range []string{"fallback", "real artwork", "real artwork"} {
		entry, err := a.saveAsset(id, ".jpg", []byte(content))
		if err != nil {
			t.Fatal(err)
		}

		asset, ok := entry.Media.(localAsset)
		if !ok {
			t.Fatal("not a durable asset")
		}

		if err = os.Chtimes(asset.file, stamp, stamp); err != nil {
			t.Fatal(err)
		}

		if err = a.saveAssetManifest(id, []library.Entry{entry}); err != nil {
			t.Fatal(err)
		}

		loaded := a.loadAssets(id)
		if len(loaded) != 1 {
			t.Fatal(loaded)
		}

		if i == 1 && loaded[0].Modified <= old.Modified {
			t.Fatal("same-second artwork replacement did not change virtual mtime")
		}

		if i == 2 && loaded[0].Modified != old.Modified {
			t.Fatal("identical artwork changed virtual mtime")
		}

		old = loaded[0]
	}

	asset, ok := old.Media.(localAsset)
	if !ok {
		t.Fatal("not a durable asset")
	}

	// An older manifest has no modified field; retain its filesystem timestamp.
	data, err := json.Marshal([]assetRecord{{Suffix: ".jpg", File: filepath.Base(asset.file), Size: asset.size}})
	if err != nil {
		t.Fatal(err)
	}

	if err = os.WriteFile(filepath.Join(a.original.DataDir, "assets", id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if loaded := a.loadAssets(id); len(loaded) != 1 || loaded[0].Modified != stamp.Unix() {
		t.Fatal("legacy manifest was not retained", loaded)
	}
}
