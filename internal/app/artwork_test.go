package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"duplex/internal/artwork"
	"duplex/internal/config"
	"duplex/internal/source"
)

func TestArtworkOverrideResetAndPersistence(t *testing.T) {
	s := fixtureCollection(1)
	a := pipelineApp(t, pipelineChannel(config.DefaultSelection()), s)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	// pipelineApp does not mount; use its durable data directory for artwork.
	a.original = a.Config.Snapshot().Config
	input := artwork.Input{Channel: "Sample", Title: "Sample", Label: "CHANNEL"}
	changed, err := a.renderPoster(testOwner, "show", input, nil)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	changed, err = a.renderPoster(testOwner, "show", input, nil)
	if err != nil || changed {
		t.Fatal("unchanged poster regenerated", err)
	}
	automatic, err := a.Artwork(t.Context(), testOwner, "show")
	if err != nil || automatic.Override || automatic.Reference == "" {
		t.Fatal(automatic, err)
	}
	imageData := image.NewRGBA(image.Rect(0, 0, 40, 60))
	imageData.Set(5, 5, color.White)
	var upload bytes.Buffer
	if err = png.Encode(&upload, imageData); err != nil {
		t.Fatal(err)
	}
	snap := a.Config.Snapshot()
	if err = a.SaveArtwork(t.Context(), testOwner, "show", snap.Revision, bytes.NewReader(upload.Bytes())); err != nil {
		t.Fatal(err)
	}
	manual, err := a.Artwork(t.Context(), testOwner, "show")
	if err != nil || !manual.Override || manual.Reference == automatic.Reference {
		t.Fatal(manual, err)
	}
	if err = a.SaveArtwork(t.Context(), testOwner, "show", snap.Revision, bytes.NewReader(upload.Bytes())); err != config.ErrConflict {
		t.Fatal("stale upload replaced settings", err)
	}
	// A new manager instance must recover the same override solely from YAML.
	file := filepath.Join(filepath.Dir(filepath.Dir(a.original.DataDir)), "config.yaml")
	reopened, err := config.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	a.Config = reopened
	if got, err := a.Artwork(t.Context(), testOwner, "show"); err != nil || got.Reference != manual.Reference {
		t.Fatal(got, err)
	}
	input.Title = "Renamed channel"
	if _, err = a.renderPoster(testOwner, "show", input, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Artwork(t.Context(), testOwner, "show"); err != nil || got.Reference != manual.Reference {
		t.Fatal("refresh replaced override", got, err)
	}
	before := a.automaticArtwork(testOwner, "show")
	input.Title = "Upstream failed"
	changed, err = a.renderPoster(testOwner, "show", input, source.ErrUnavailable)
	if err != nil || changed || a.automaticArtwork(testOwner, "show") != before {
		t.Fatal("source failure replaced last valid poster", err)
	}
	if err = a.ResetArtwork(t.Context(), testOwner, "show", a.Config.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	reset, err := a.Artwork(t.Context(), testOwner, "show")
	if err != nil || reset.Override || reset.Reference != before.Reference {
		t.Fatal(reset, err)
	}
	if err = os.Remove(reset.Path); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Artwork(t.Context(), testOwner, "show"); err == nil {
		t.Fatal("missing durable blob hidden")
	}
	if _, err = a.Artwork(t.Context(), "unknown", "show"); err == nil {
		t.Fatal("unknown channel")
	}
	if err = a.ResetArtwork(t.Context(), testOwner, "year:1800", a.Config.Snapshot().Revision); err == nil {
		t.Fatal("unknown season accepted")
	}
}
