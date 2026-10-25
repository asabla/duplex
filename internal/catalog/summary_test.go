package catalog

import "testing"

func TestEmptySummary(t *testing.T) {
	s := collectionStore(t)
	counts, err := s.Summary(t.Context(), Visibility{Channels: []string{"owner"}})
	if err != nil || counts != (Counts{}) {
		t.Fatal(counts, err)
	}
}

func TestSummaryAndSelectionCountCanonicalVideosOnce(t *testing.T) {
	s := collectionStore(t)
	for _, id := range []string{"ready", "failed", "unavailable", "outside"} {
		preparedVideo(t, s, id, id)
	}
	if err := s.ExposePrepared(t.Context(), "ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE videos SET preparation='failed' WHERE id='failed'`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"PLone", "PLtwo"} {
		if err := s.SavePlaylist(t.Context(), Playlist{ID: id, ChannelID: "owner", Title: id}); err != nil {
			t.Fatal(err)
		}
		if err := reconcileMembers(t.Context(), t, s, id, []testMember{{"ready", 1}, {"failed", 2}, {"unavailable", 3}}, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE videos SET state='unavailable' WHERE id='unavailable'`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		visibility Visibility
		want       Counts
	}{
		{"empty", Visibility{}, Counts{Discovered: 4}},
		{"uploads and duplicate playlists", Visibility{Channels: []string{"uploader"}, PlaylistChannels: []string{"owner"}}, Counts{Discovered: 4, Selected: 3, Ready: 1, Failed: 1}},
		{"playlists only", Visibility{PlaylistChannels: []string{"owner"}}, Counts{Discovered: 4, Selected: 3, Ready: 1, Failed: 1}},
		{"one skipped", Visibility{PlaylistChannels: []string{"owner"}, SkippedPlaylists: []string{"PLone"}}, Counts{Discovered: 4, Selected: 3, Ready: 1, Failed: 1}},
		{"all skipped", Visibility{PlaylistChannels: []string{"owner"}, SkippedPlaylists: []string{"PLone", "PLtwo"}}, Counts{Discovered: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Summary(t.Context(), tc.visibility)
			if err != nil || got != tc.want {
				t.Fatalf("counts = %+v, want %+v: %v", got, tc.want, err)
			}
			for _, inclusion := range []string{"selected", "outside"} {
				page, err := s.VideosPage(t.Context(), VideoFilter{Visibility: tc.visibility, Inclusion: inclusion})
				want := tc.want.Selected
				if inclusion == "outside" {
					want = tc.want.Discovered - want
				}
				if err != nil || page.Total != want || len(page.Items) != want {
					t.Fatalf("%s: total=%d rows=%d, want %d: %v", inclusion, page.Total, len(page.Items), want, err)
				}
			}
		})
	}
}
