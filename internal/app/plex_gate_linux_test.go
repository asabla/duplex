//go:build linux

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"duplex/internal/config"
	"duplex/internal/library"
	"duplex/internal/media"
	"duplex/internal/source"
)

// TestPlexPlaylistQualification exposes the real catalogue provider, muxer,
// cache, and artwork to the separately claimed qualification Plex server. The
// external driver checks Plex's results, then supplies each controlled phase.
// It uses only the supplied isolated-server credential and test library section.
func TestPlexPlaylistQualification(t *testing.T) {
	const qualificationOwner = "UCbbbbbbbbbbbbbbbbbbbbbb"

	root := os.Getenv("DUPLEX_PLEX_GATE_DIR")
	if root == "" {
		t.Skip("requires the isolated Plex qualification mount and external driver")
	}

	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		t.Fatal("qualification directory must be an absolute clean path")
	}

	app, adapter, _ := mediaPipeline(t)
	ch := pipelineChannel(config.Selection{Mode: "all"})
	ch.ID, ch.URL = qualificationOwner, "https://www.youtube.com/channel/"+qualificationOwner
	snapshot := app.Config.Snapshot()

	snapshot.Config.Channels[0] = ch
	if section := os.Getenv("DUPLEX_PLEX_GATE_SECTION"); section != "" {
		snapshot.Config.Plex = config.Plex{URL: "http://127.0.0.1:32400", Section: section, TokenFile: "/evidence/plex-test-token"} // #nosec G101 -- This is a private mounted credential filename, not a credential value.
	}

	if err := app.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}

	base := adapter.metadata["video000000"]
	base.ChannelID = qualificationOwner
	base.Channel = "duplex playlist qualification"

	for i := range 3 {
		id := fmt.Sprintf("video%06d", i)
		metadata := base
		metadata.ID, metadata.Title = id, fmt.Sprintf("Qualification recording %d", i+1)
		metadata.Timestamp += float64(i * 86400)
		metadata.Duration = 30
		adapter.metadata[id] = metadata
		adapter.entries["videos"] = append(adapter.entries["videos"], source.Entry{ID: id, Title: metadata.Title})
	}

	adapter.lists = []source.Playlist{{ID: "PLfixture0001", Title: "Ordered collection"}, {ID: "PLfixture0002", Title: "Shared recording"}}
	adapter.entries["PLfixture0001"] = []source.Entry{{ID: "video000000"}, {ID: "video000001"}, {ID: "video000001"}, {ID: "video000002"}}
	adapter.entries["PLfixture0002"] = []source.Entry{{ID: "video000000"}}

	if err := app.Sync(t.Context(), qualificationOwner); err != nil {
		t.Fatal(err)
	}

	if err := app.Catalog.EnsureChannel(t.Context(), qualificationOwner, "duplex playlist qualification"); err != nil {
		t.Fatal(err)
	}

	for i := range 3 {
		if err := app.prepareRecording(t.Context(), fmt.Sprintf("video%06d", i), false); err != nil {
			t.Fatal(err)
		}
	}

	if err := app.renderArtwork(t.Context(), ch); err != nil {
		t.Fatal(err)
	}

	mount, err := library.MountProvider(filepath.Join(root, "media"), catalogueFS{app: app})
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		app.cancel()
		app.wg.Wait()

		if err := mount.Close(); err != nil {
			t.Error(err)
		}
	}()

	if !mount.Ready() {
		t.Fatal("qualification mount is not ready")
	}

	app.mount = mount

	app.wg.Add(1)
	go app.metadataWorker(app.ctx)

	if app.Config.Snapshot().Config.Plex.URL != "" {
		if err = app.Scan(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	acknowledge := func(phase string) {
		if err := os.WriteFile(filepath.Join(root, "phase"), []byte(phase), 0o644); err != nil { //nolint:gosec // G306: The isolated driver reads only a non-secret fixture phase name.
			t.Fatal(err)
		}

		t.Log("Plex qualification phase:", phase)
	}
	acknowledge("initial")

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Hour)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			t.Fatal("external Plex qualification driver did not finish", ctx.Err())
		case <-ticker.C:
		}

		commandPath := filepath.Join(root, "command")

		data, err := os.ReadFile(commandPath) //nolint:gosec // G304: This is the explicitly configured isolated test control directory.
		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if err != nil {
			t.Fatal(err)
		}

		if err = os.Remove(commandPath); err != nil { // #nosec G703 -- The fixed command filename is beneath the explicit isolated qualification directory.
			t.Fatal(err)
		}

		phase := strings.TrimSpace(string(data))
		switch phase {
		case "reorder":
			adapter.lists[0].Title = "Renamed ordered collection"
			adapter.entries["PLfixture0001"] = []source.Entry{{ID: "video000002"}, {ID: "video000000"}, {ID: "video000001"}}

			err = app.Sync(ctx, qualificationOwner)
			if err == nil && app.Config.Snapshot().Config.Plex.URL != "" {
				err = app.Scan(ctx)
			}
		case "restore":
			adapter.entries["PLfixture0001"] = []source.Entry{{ID: "video000000"}, {ID: "video000001"}, {ID: "video000002"}}

			err = app.Sync(ctx, qualificationOwner)
			if err == nil && app.Config.Snapshot().Config.Plex.URL != "" {
				err = app.Scan(ctx)
			}
		case "remove", "return":
			adapter.entries["PLfixture0001"] = []source.Entry{{ID: "video000002"}, {ID: "video000001"}}
			if phase == "return" {
				adapter.entries["PLfixture0001"] = []source.Entry{{ID: "video000001"}, {ID: "video000000"}, {ID: "video000002"}}
			}

			err = app.Sync(ctx, qualificationOwner)
			if err == nil && app.Config.Snapshot().Config.Plex.URL != "" {
				err = app.Scan(ctx)
			}
		case "artwork":
			var data bytes.Buffer

			picture := image.NewUniform(color.RGBA{R: 190, G: 40, B: 20, A: 255})
			canvas := image.NewRGBA(image.Rect(0, 0, 200, 300))

			for y := range 300 {
				for x := range 200 {
					canvas.Set(x, y, picture.At(x, y))
				}
			}

			err = png.Encode(&data, canvas)
			if err == nil {
				err = app.SaveArtwork(ctx, qualificationOwner, "show", app.Config.Snapshot().Revision, bytes.NewReader(data.Bytes()))
			}

			if err == nil {
				err = app.SaveArtwork(ctx, qualificationOwner, "playlist:PLfixture0001", app.Config.Snapshot().Revision, bytes.NewReader(data.Bytes()))
			}
		case "rebind":
			app.mu.Lock()
			app.reps = map[string]media.Representation{}
			app.metadata = map[string]metadataLease{}
			app.mu.Unlock()
		case "stop":
			acknowledge(phase)
			return
		default:
			t.Fatal("unknown qualification phase")
		}

		if err != nil {
			t.Fatal(phase, err)
		}

		acknowledge(phase)
	}
}
