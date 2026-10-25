package catalog

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"duplex/internal/source"
)

func collectionStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "catalogue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, id := range []string{"owner", "uploader"} {
		if err = s.EnsureChannel(t.Context(), id, id); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func preparedVideo(t *testing.T, s *Store, id, title string) Prepared {
	t.Helper()
	m := source.Metadata{ID: id, ChannelID: "uploader", Channel: "Uploader", Title: title, UploadDate: "20261025", Duration: 60, LiveStatus: "not_live"}
	if err := s.SaveMetadata(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	p := Prepared{Metadata: m, Revision: "revision-" + id, Height: 2160, Size: 4096}
	if err := s.SavePrepared(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlaylistSeasonIdentityOrderAndDuplicates(t *testing.T) {
	s := collectionStore(t)
	ctx := t.Context()
	for _, id := range []string{"a", "b", "c"} {
		preparedVideo(t, s, id, id)
	}
	p := Playlist{ID: "PLfirst", ChannelID: "owner", Title: "First"}
	if err := s.SavePlaylist(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlaylist(ctx, Playlist{ID: "PLsecond", ChannelID: "owner", Title: "Second"}); err != nil {
		t.Fatal(err)
	}
	selectAll := map[string]bool{"a": true, "b": true, "c": true}
	if err := s.ReplacePlaylistMembers(ctx, p.ID, []Member{{"b", 1}, {"a", 2}, {"b", 3}, {"c", 4}}, selectAll, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplacePlaylistMembers(ctx, "PLsecond", []Member{{"a", 1}}, selectAll, true); err != nil {
		t.Fatal(err)
	}
	episodes, err := s.Episodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 4 || episodes[0].VideoID != "b" || episodes[1].VideoID != "a" || episodes[2].VideoID != "c" {
		t.Fatal(episodes)
	}
	identity := episodes[1].OccurrenceID
	if identity == episodes[3].OccurrenceID || episodes[1].Revision != episodes[3].Revision {
		t.Fatal("occurrence identity or media deduplication")
	}
	p.Title = "Renamed"
	if err = s.SavePlaylist(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = s.ReplacePlaylistMembers(ctx, p.ID, []Member{{"a", 1}, {"c", 2}}, selectAll, true); err != nil {
		t.Fatal(err)
	}
	episodes, err = s.Episodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 3 || episodes[0].OccurrenceID != identity || episodes[0].Number != 1 || episodes[0].Season != 1 || episodes[0].SeasonTitle != "Renamed" {
		t.Fatal(episodes)
	}
	pls, err := s.Playlists(ctx, "owner")
	if err != nil || len(pls) != 2 {
		t.Fatal(pls, err)
	}
	if err = s.SavePlaylist(ctx, Playlist{ID: p.ID, ChannelID: "uploader", Title: "Wrong owner"}); err == nil {
		t.Fatal("playlist owner changed")
	}
}

func TestDiscoveryAndPreparationRetainPreviouslyPublishedMedia(t *testing.T) {
	s := collectionStore(t)
	ctx := t.Context()
	preparedVideo(t, s, "a", "Old")
	if err := s.ExposePrepared(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Discover(ctx, "owner", []source.Entry{{ID: "a", Title: "New"}, {ID: "b", Title: "New discovery"}}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Videos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Revision == "" || !rows[0].Exposed {
		t.Fatal("discovery removed usable media", rows)
	}
	if err = s.SetPreparation(ctx, "a", "failed", "Retry required"); err != nil {
		t.Fatal(err)
	}
	episodes, err := s.Episodes(ctx)
	if err != nil || len(episodes) != 1 || episodes[0].Revision != "revision-a" {
		t.Fatal("failed preparation removed old revision", err)
	}
	if err = s.SetPreparation(ctx, "a", "unknown", ""); err == nil {
		t.Fatal("invalid preparation state")
	}
	if err = s.SavePrepared(ctx, Prepared{}); err == nil {
		t.Fatal("empty media accepted")
	}
	if err = s.SelectUploads(ctx, "uploader", nil); err != nil {
		t.Fatal(err)
	}
	if episodes, err = s.Episodes(ctx); err != nil || len(episodes) != 0 {
		t.Fatal("selection reconciliation", episodes, err)
	}
}

func TestBoundedVideoQueriesAndConfigurationVisibility(t *testing.T) {
	s := collectionStore(t)
	ctx := t.Context()
	for i := range 61 {
		id := string(rune('A' + i))
		preparedVideo(t, s, id, "Title "+id)
		if err := s.ExposePrepared(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	visible := Visibility{Channels: []string{"uploader"}}
	page, err := s.VideosPage(ctx, VideoFilter{Visibility: visible, Page: 2, PerPage: 25, Inclusion: "selected"})
	if err != nil || page.Total != 61 || len(page.Items) != 25 || page.Page != 2 {
		t.Fatal(page, err)
	}
	page, err = s.VideosPage(ctx, VideoFilter{Visibility: visible, Page: 999, PerPage: 50, Inclusion: "selected"})
	if err != nil || page.Page != 2 || len(page.Items) != 11 {
		t.Fatal(page, err)
	}
	page, err = s.VideosPage(ctx, VideoFilter{Query: "Title A", PerPage: 999, Sort: "title"})
	if err != nil || page.Total != 2 || page.PerPage != 25 {
		t.Fatal(page, err)
	}
	if _, err = s.VideosPage(ctx, VideoFilter{Sort: "title;DROP TABLE videos"}); err == nil {
		t.Fatal("invalid sort")
	}
	if _, err = s.VideosPage(ctx, VideoFilter{Inclusion: "unknown"}); err == nil {
		t.Fatal("invalid inclusion")
	}
	page, err = s.VideosPage(ctx, VideoFilter{Inclusion: "selected"})
	if err != nil || page.Total != 0 {
		t.Fatal("unfollowed videos selected", page, err)
	}
	c, err := s.Summary(ctx, visible)
	if err != nil || c.Discovered != 61 || c.Selected != 61 || c.Ready != 61 {
		t.Fatal(c, err)
	}
}

func TestPartialMembershipWindowDoesNotDeleteUnseenIdentity(t *testing.T) {
	s := collectionStore(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		preparedVideo(t, s, id, id)
	}
	if err := s.SavePlaylist(ctx, Playlist{ID: "PLfirst", ChannelID: "owner", Title: "First"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplacePlaylistMembers(ctx, "PLfirst", []Member{{"a", 1}, {"b", 2}}, map[string]bool{"a": true, "b": true}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplacePlaylistMembers(ctx, "PLfirst", []Member{{"a", 1}}, map[string]bool{"a": true}, false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM playlist_members`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	v := Visibility{PlaylistChannels: []string{"owner"}}
	c, err := s.Summary(ctx, v)
	if err != nil || c.Selected != 1 {
		t.Fatal(c, err)
	}
	v.SkippedPlaylists = []string{"PLfirst"}
	c, err = s.Summary(ctx, v)
	if err != nil || c.Selected != 0 {
		t.Fatal(c, err)
	}
	page, err := s.VideosPage(ctx, VideoFilter{Channel: "owner", Playlist: "PLfirst", Lifecycle: "recorded", Preparation: "ready", Sort: "oldest"})
	if err != nil || page.Total != 2 {
		t.Fatal(page, err)
	}
	if err = s.BindOccurrence(ctx, "occurrence", "server", "42", "guid"); err != nil {
		t.Fatal(err)
	}
	if err = s.ChannelArtwork(ctx, "owner", "https://yt3.ggpht.com/avatar", "https://yt3.ggpht.com/banner"); err != nil {
		t.Fatal(err)
	}
	if err = s.JobProgress(ctx, 0, "discovery", 2, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.JobProgress(ctx, 1, "preparing", 2, 1); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err = s.DB.QueryRowContext(t.Context(), `SELECT metadata FROM metadata_revisions LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "videoplayback") {
		t.Fatal("signed locator stored")
	}
	if !reflect.DeepEqual(PlaylistOccurrenceID("a", "b", "c"), "youtube-playlist:a:b:c") {
		t.Fatal("unstable occurrence")
	}
}

func TestMetadataCannotDetachAnEstablishedUploader(t *testing.T) {
	s := collectionStore(t)
	ctx := t.Context()
	m := source.Metadata{ID: "pending", ChannelID: "uploader", Title: "Pending", LiveStatus: "post_live"}
	if err := s.SaveMetadata(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.ChannelID = ""
	if err := s.SaveMetadata(ctx, m); err == nil {
		t.Fatal("missing uploader accepted")
	}
	m.ChannelID = "owner"
	if err := s.SaveMetadata(ctx, m); err == nil {
		t.Fatal("uploader changed before media preparation")
	}
	v, err := s.Video(ctx, m.ID)
	if err != nil || v.ChannelID != "uploader" {
		t.Fatal(v, err)
	}
}
