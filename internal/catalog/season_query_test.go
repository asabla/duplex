package catalog

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"duplex/internal/source"
)

func TestArtworkSeasonPagesKeepTitlesAndUnavailableIdentities(t *testing.T) {
	s := collectionStore(t)
	for i := range 60 {
		if err := s.SavePlaylist(t.Context(), Playlist{ID: fmt.Sprintf("PLfixture%04d", i), ChannelID: "owner", Title: fmt.Sprintf("Named season %02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	metadata := source.Metadata{ID: "year-video", ChannelID: "owner", Title: "Recording", UploadDate: "20261025", Duration: 60}
	if err := s.SaveMetadata(t.Context(), metadata); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePrepared(t.Context(), Prepared{Metadata: metadata, Revision: "year-revision", Height: 1080, Size: 4096}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ArtworkSeasons(t.Context(), SeasonFilter{Channel: "owner", Page: 2})
	if err != nil || page.Total != 61 || page.PerPage != 25 || len(page.Items) != 25 || page.Items[0].Number != 26 || page.Items[24].Number != 50 {
		t.Fatal(page, err)
	}
	last, err := s.ArtworkSeasons(t.Context(), SeasonFilter{Channel: "owner", Page: 999, PerPage: 25})
	if err != nil || last.Page != 3 || len(last.Items) != 11 || last.Items[10].Title != "Year 2026" || last.Items[10].Kind != "year" {
		t.Fatal(last, err)
	}
	found, err := s.ArtworkSeasons(t.Context(), SeasonFilter{Channel: "owner", Query: "NAMED SEASON 59", Page: 999, PerPage: 100})
	if err != nil || found.Page != 1 || found.Total != 1 || found.Items[0].Scope != "playlist:PLfixture0059" {
		t.Fatal(found, err)
	}
	if err = s.ReconcilePlaylists(t.Context(), 123, "owner"); err != nil {
		t.Fatal(err)
	}
	selected, err := s.ArtworkSeasons(t.Context(), SeasonFilter{Channel: "owner", Scope: "playlist:PLfixture0059"})
	if err != nil || selected.Total != 1 || selected.Items[0].Title != "Named season 59" {
		t.Fatal("missing playlist lost its editable season", selected, err)
	}
	if err = s.SavePlaylist(t.Context(), Playlist{ID: "PLfixture0059", ChannelID: "owner", Title: "Renamed collection"}); err != nil {
		t.Fatal(err)
	}
	selected, err = s.ArtworkSeasons(t.Context(), SeasonFilter{Channel: "owner", Scope: "playlist:PLfixture0059", PerPage: -1})
	if err != nil || selected.Items[0].Number != 60 || selected.Items[0].Title != "Renamed collection" || selected.PerPage != 25 {
		t.Fatal(selected, err)
	}
	empty, err := s.ArtworkSeasons(t.Context(), SeasonFilter{Channel: "owner", Query: "'%; DROP TABLE playlists;"})
	if err != nil || empty.Total != 0 || empty.Page != 1 || len(empty.Items) != 0 {
		t.Fatal(empty, err)
	}
	empty, err = s.ArtworkSeasons(t.Context(), SeasonFilter{Channel: "unknown"})
	if err != nil || empty.Total != 0 {
		t.Fatal(empty, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = s.ArtworkSeasons(ctx, SeasonFilter{Channel: "owner"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPlaylistAvailabilityUsesTheLastCompleteRootSnapshot(t *testing.T) {
	s := collectionStore(t)
	for _, id := range []string{"PLfirst", "PLmissing"} {
		if err := s.SavePlaylist(t.Context(), Playlist{ID: id, ChannelID: "owner", Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DiscoverPlaylist(t.Context(), 7, Playlist{ID: "PLfirst", ChannelID: "owner", Title: "Available"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcilePlaylists(t.Context(), 7, "owner"); err != nil {
		t.Fatal(err)
	}
	policies := []ChannelPolicy{{ID: "owner", Mode: "all", Enabled: true}}
	for _, tc := range []struct{ availability, id string }{{"available", "PLfirst"}, {"missing", "PLmissing"}} {
		page, err := s.PlaylistsPage(t.Context(), PlaylistFilter{Policies: policies, Availability: tc.availability})
		if err != nil || page.Total != 1 || page.Items[0].ID != tc.id {
			t.Fatal(page, err)
		}
	}
	if _, err := s.PlaylistsPage(t.Context(), PlaylistFilter{Policies: policies, Availability: "invalid"}); err == nil {
		t.Fatal("invalid availability accepted")
	}
}
