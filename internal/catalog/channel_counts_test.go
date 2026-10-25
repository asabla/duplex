package catalog

import (
	"testing"
)

func TestChannelCountsRespectOwnerDeduplicationAndSelection(t *testing.T) {
	s := collectionStore(t)

	ctx := t.Context()
	for _, id := range []string{"first", "second", "excluded"} {
		preparedVideo(t, s, id, id)
	}

	if err := s.SelectUploads(ctx, "uploader", []string{"first"}); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"PLone", "PLtwo"} {
		if err := s.SavePlaylist(ctx, Playlist{ID: id, ChannelID: "owner", Title: id}); err != nil {
			t.Fatal(err)
		}

		if err := reconcileMembers(ctx, t, s, id, []testMember{{"first", 1}, {"second", 2}}, true); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.SetPreparation(ctx, "second", "failed", "source failed"); err != nil {
		t.Fatal(err)
	}

	visible := Visibility{Channels: []string{"owner", "uploader"}, PlaylistChannels: []string{"owner"}}

	rows, err := s.ChannelCounts(ctx, []string{"owner", "uploader"}, visible)
	if err != nil || rows["owner"].Selected != 2 || rows["owner"].Ready != 1 || rows["owner"].Failed != 1 || rows["uploader"].Selected != 1 {
		t.Fatal(rows, err)
	}

	visible.SkippedPlaylists = []string{"PLone", "PLtwo"}

	rows, err = s.ChannelCounts(ctx, []string{"owner", "uploader"}, visible)
	if err != nil || rows["owner"].Selected != 0 || rows["uploader"].Ready != 1 {
		t.Fatal(rows, err)
	}

	rows, err = s.ChannelCounts(ctx, nil, visible)
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}

	if _, err = s.ChannelCounts(ctx, make([]string, 101), visible); err == nil {
		t.Fatal("unbounded summary accepted")
	}

	if err = s.DB.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err = s.ChannelCounts(ctx, []string{"owner"}, visible); err == nil {
		t.Fatal("closed database accepted")
	}
}
