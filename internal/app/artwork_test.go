package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

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

	if err = a.SaveArtwork(t.Context(), testOwner, "show", snap.Revision, bytes.NewReader(upload.Bytes())); !errors.Is(err, config.ErrConflict) {
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

type brandingSource struct {
	*collectionSource
	channel source.Channel
	failure error
	calls   int
}

func (s *brandingSource) ResolveChannel(ctx context.Context, _ string) (source.Channel, error) {
	s.calls++
	if s.failure != nil {
		return source.Channel{}, s.failure
	}

	return s.channel, ctx.Err()
}

func TestAutomaticBrandingUsesChannelAndSeasonInputs(t *testing.T) {
	collection := fixtureCollection(1)
	collection.lists = []source.Playlist{{ID: "PLfixture0001", Title: "Named collection", Thumbnail: "https://i.ytimg.com/picture"}, {ID: "PLfixture0002", Title: "Missing picture"}}
	collection.entries["PLfixture0001"] = collection.entries["videos"]
	collection.entries["PLfixture0002"] = collection.entries["videos"]
	adapter := &brandingSource{collectionSource: collection, channel: source.Channel{ID: testOwner, Avatar: "https://i.ytimg.com/picture", Banner: "https://i.ytimg.com/picture"}}

	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), adapter)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	projectionVideo(t, a, "video000000", 1)

	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 64, 36))); err != nil {
		t.Fatal(err)
	}

	previous := http.DefaultTransport
	http.DefaultTransport = assetFixtureTransport{"/picture": {data: picture.Bytes()}}

	t.Cleanup(func() { http.DefaultTransport = previous })

	state := artworkSchedule{brandingAt: map[string]time.Time{}, rendered: map[string]time.Time{}, versions: map[string]string{}}
	channel := a.Config.Snapshot().Config.Channels[0]
	a.updateChannelArtwork(t.Context(), channel, &state)

	if adapter.calls != 1 {
		t.Fatal("branding was not fetched once", adapter.calls)
	}

	seasons, err := a.Catalog.Seasons(t.Context(), testOwner)
	if err != nil || len(seasons) != 3 {
		t.Fatal(seasons, err)
	}

	scopes := []string{"show"}
	for scope := range seasons {
		scopes = append(scopes, scope)
	}

	references := map[string]bool{}

	for _, scope := range scopes {
		preview, err := a.Artwork(t.Context(), testOwner, scope)
		if err != nil || preview.Reference == "" || references[preview.Reference] {
			t.Fatal(scope, preview, err)
		}

		references[preview.Reference] = true

		data, err := os.ReadFile(preview.Path)
		if err != nil {
			t.Fatal(err)
		}

		info, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || info.Width*3 != info.Height*2 {
			t.Fatal("poster aspect ratio", scope, info, err)
		}
	}

	before := a.automaticArtwork(testOwner, "show")
	a.updateChannelArtwork(t.Context(), channel, &state)

	if adapter.calls != 1 || a.automaticArtwork(testOwner, "show").Reference != before.Reference {
		t.Fatal("unchanged branding was fetched or rendered again")
	}

	if err := a.Catalog.EnsureChannel(t.Context(), testOwner, "Renamed channel"); err != nil {
		t.Fatal(err)
	}

	a.updateChannelArtwork(t.Context(), channel, &state)

	if a.automaticArtwork(testOwner, "show").Reference == before.Reference {
		t.Fatal("changed title did not invalidate the poster")
	}

	before = a.automaticArtwork(testOwner, "show")
	if err := a.Catalog.ChannelArtwork(t.Context(), testOwner, "https://i.ytimg.com/offline", ""); err != nil {
		t.Fatal(err)
	}

	if err := a.renderArtwork(t.Context(), channel); err != nil {
		t.Fatal(err)
	}

	if a.automaticArtwork(testOwner, "show").Reference != before.Reference {
		t.Fatal("failed branding retrieval replaced the last valid poster")
	}
}
