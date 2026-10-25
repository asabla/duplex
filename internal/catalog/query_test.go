package catalog

import (
	"testing"
)

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
