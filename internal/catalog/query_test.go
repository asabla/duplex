package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestSelectedPagesFilterSharedVideosBeforePagination(t *testing.T) {
	s := collectionStore(t)
	members := []testMember{}
	want := []string{}
	for i := range 90 {
		id := fmt.Sprintf("video%03d", i)
		title := "Drop " + id
		if i%2 == 0 {
			title = "Keep " + id
			if i%3 != 0 {
				want = append(want, id)
			}
		}
		preparedVideo(t, s, id, title)
		if i%2 == 0 {
			if err := s.ExposePrepared(t.Context(), id); err != nil {
				t.Fatal(err)
			}
		}
		if i%3 == 0 {
			if _, err := s.DB.ExecContext(t.Context(), `UPDATE videos SET preparation='failed' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
		}
		members = append(members, testMember{id, i + 1})
	}
	for _, id := range []string{"PLone", "PLtwo"} {
		if err := s.SavePlaylist(t.Context(), Playlist{ID: id, ChannelID: "owner", Title: id}); err != nil {
			t.Fatal(err)
		}
		if err := reconcileMembers(t.Context(), t, s, id, members, true); err != nil {
			t.Fatal(err)
		}
	}
	slices.Reverse(want)
	page, err := s.VideosPage(t.Context(), VideoFilter{
		Visibility: Visibility{Channels: []string{"uploader"}, PlaylistChannels: []string{"owner"}},
		Inclusion:  "selected", Channel: "owner", Playlist: "PLone", Query: "KEEP", Preparation: "ready", Lifecycle: "recorded", Sort: "title_desc", PerPage: 25, Page: 2,
	})
	if err != nil || page.Total != len(want) || page.Page != 2 || len(page.Items) != len(want)-25 {
		t.Fatal(page.Total, page.Page, len(page.Items), want, err)
	}
	for i, row := range page.Items {
		if row.ID != want[i+25] || !row.Exposed {
			t.Fatal(i, row.ID, row.Exposed, want[i+25])
		}
	}
}

func TestPolicyFilteredPlaylistPages(t *testing.T) {
	s := collectionStore(t)
	ctx := t.Context()
	for _, p := range []Playlist{{ID: "PLone", ChannelID: "owner", Title: "Alpha"}, {ID: "PLtwo", ChannelID: "owner", Title: "Beta"}, {ID: "PLthree", ChannelID: "uploader", Title: "Gamma"}} {
		if err := s.SavePlaylist(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	policies := []ChannelPolicy{{ID: "owner", Mode: "latest", Limit: 50, Enabled: true, Overrides: []PlaylistOverride{{ID: "PLtwo", Mode: "all"}}}, {ID: "uploader", Mode: "latest", Limit: 30, Enabled: false}}
	all, err := s.PlaylistsPage(ctx, PlaylistFilter{Policies: policies})
	if err != nil || all.Total != 3 || all.PerPage != 25 {
		t.Fatal(all, err)
	}
	latest, err := s.PlaylistsPage(ctx, PlaylistFilter{Policies: policies, Mode: "latest"})
	if err != nil || latest.Total != 1 || !latest.Items[0].Inherited || latest.Items[0].Limit != 50 {
		t.Fatal(latest, err)
	}
	override, err := s.PlaylistsPage(ctx, PlaylistFilter{Policies: policies, Channel: "owner", Mode: "all", Sort: "season", Page: 99})
	if err != nil || override.Total != 1 || override.Items[0].Inherited || override.Items[0].ID != "PLtwo" {
		t.Fatal(override, err)
	}
	skipped, err := s.PlaylistsPage(ctx, PlaylistFilter{Policies: policies, Mode: "skip", Query: "gamma", Sort: "updated", PerPage: 100})
	if err != nil || skipped.Total != 1 {
		t.Fatal(skipped, err)
	}
	if empty, err := s.PlaylistsPage(ctx, PlaylistFilter{}); err != nil || empty.Total != 0 {
		t.Fatal(empty, err)
	}
	if _, err = s.PlaylistsPage(ctx, PlaylistFilter{Policies: policies, Sort: "untrusted"}); err == nil {
		t.Fatal("invalid sort")
	}
	if _, err = s.PlaylistsPage(ctx, PlaylistFilter{Policies: policies, Failure: "failed"}); err != nil {
		t.Fatal(err)
	}
}

func TestChannelAndJobReadPages(t *testing.T) {
	s := collectionStore(t)
	ctx := t.Context()
	channels, err := s.ChannelsPage(ctx, ChannelFilter{IDs: []string{"owner", "uploader"}, Query: "OWNER", Sort: "updated", Page: 999})
	if err != nil || channels.Total != 1 || channels.Items[0].ID != "owner" {
		t.Fatal(channels, err)
	}
	if _, err = s.ChannelsPage(ctx, ChannelFilter{Sort: "injection"}); err == nil {
		t.Fatal("invalid sort")
	}
	id, err := s.StartJob(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishJob(ctx, id, "owner", nil); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.FilterJobs(ctx, JobFilter{Channel: "owner", State: "complete", Sort: "oldest", Page: 99})
	if err != nil || jobs.Total != 1 || jobs.Page != 1 || jobs.Items[0].ID != id {
		t.Fatal(jobs, err)
	}
	if _, err = s.FilterJobs(ctx, JobFilter{Sort: "injection"}); err == nil {
		t.Fatal("invalid sort")
	}
}

func TestLegacyJobsPageRetainsOldHistoryWithBoundedRows(t *testing.T) {
	s := collectionStore(t)
	for range 105 {
		id, err := s.StartJob(t.Context(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		if err = s.FinishJob(t.Context(), id, "owner", nil); err != nil {
			t.Fatal(err)
		}
	}
	jobs, total, err := s.JobsPage(t.Context(), 999, 25)
	if err != nil || total != 105 || len(jobs) != 5 || jobs[0].ID != 5 || jobs[4].ID != 1 {
		t.Fatal(total, jobs, err)
	}
	jobs, total, err = s.JobsPage(t.Context(), -1, 999)
	if err != nil || total != 105 || len(jobs) != 100 || jobs[0].ID != 105 || jobs[99].ID != 6 {
		t.Fatal(total, len(jobs), err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err = s.JobsPage(ctx, 1, 25); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestReadPagesSortInBothDirectionsBeforePagination(t *testing.T) {
	s := collectionStore(t)
	for _, item := range []struct {
		id, title string
		stamp     int
	}{{"a", "Alpha", 10}, {"z", "Zulu", 20}} {
		preparedVideo(t, s, item.id, item.title)
		if _, err := s.DB.ExecContext(t.Context(), `UPDATE videos SET updated=? WHERE id=?`, item.stamp, item.id); err != nil {
			t.Fatal(err)
		}
		if err := s.SavePlaylist(t.Context(), Playlist{ID: "PL" + item.id, Title: item.title, ChannelID: "owner"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.ExecContext(t.Context(), `UPDATE playlists SET synced_at=? WHERE id=?`, item.stamp, "PL"+item.id); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ order, first string }{{"title", "a"}, {"title_desc", "z"}, {"updated", "z"}, {"updated_oldest", "a"}} {
		page, err := s.VideosPage(t.Context(), VideoFilter{Inclusion: "all", Sort: tc.order})
		if err != nil || len(page.Items) != 2 || page.Items[0].ID != tc.first {
			t.Fatal(tc, page, err)
		}
		playlists, err := s.PlaylistsPage(t.Context(), PlaylistFilter{Policies: []ChannelPolicy{{ID: "owner", Mode: "all", Enabled: true}}, Sort: tc.order})
		if err != nil || playlists.Items[0].ID != "PL"+tc.first {
			t.Fatal(tc, playlists, err)
		}
	}
	page, err := s.PlaylistsPage(t.Context(), PlaylistFilter{Policies: []ChannelPolicy{{ID: "owner", Mode: "all", Enabled: true}}, Sort: "season_desc"})
	if err != nil || page.Items[0].ID != "PLz" {
		t.Fatal(page, err)
	}
	for _, tc := range []struct{ order, first string }{{"title", "owner"}, {"title_desc", "uploader"}, {"updated", "uploader"}, {"updated_oldest", "owner"}} {
		if _, err := s.DB.ExecContext(t.Context(), `UPDATE channels SET synced_at=CASE id WHEN 'owner' THEN 10 ELSE 20 END`); err != nil {
			t.Fatal(err)
		}
		channels, err := s.ChannelsPage(t.Context(), ChannelFilter{IDs: []string{"owner", "uploader"}, Sort: tc.order})
		if err != nil || channels.Items[0].ID != tc.first {
			t.Fatal(tc, channels, err)
		}
	}
}
