package catalog

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"duplex/internal/source"
)

func TestProjectionTimesChangeOnlyWithPublishedContent(t *testing.T) {
	s := collectionStore(t)
	p := preparedVideo(t, s, "a", "Original")
	if err := s.ExposePrepared(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	before, err := s.Video(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveMetadata(t.Context(), p.Metadata); err != nil {
		t.Fatal(err)
	}
	if err = s.SavePrepared(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.Video(t.Context(), "a")
	if err != nil || unchanged.Updated != before.Updated {
		t.Fatal("unchanged proof changed file time", before.Updated, unchanged.Updated, err)
	}
	var revisions int
	if err = s.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM metadata_revisions WHERE video_id='a'`).Scan(&revisions); err != nil || revisions != 1 {
		t.Fatal("unchanged metadata added a revision", revisions, err)
	}
	p.Metadata.Title = "Renamed"
	if err = s.SaveMetadata(t.Context(), p.Metadata); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Video(t.Context(), "a")
	if err != nil || changed.Updated <= unchanged.Updated {
		t.Fatal("title change has stale time", changed, err)
	}
}

func TestPlaylistReorderAndRenameAdvanceOccurrenceTimes(t *testing.T) {
	s := collectionStore(t)
	for _, id := range []string{"a", "b"} {
		preparedVideo(t, s, id, id)
	}
	if err := s.SavePlaylist(t.Context(), Playlist{ID: "PLfixture", ChannelID: "owner", Title: "First"}); err != nil {
		t.Fatal(err)
	}
	if err := reconcileMembers(t.Context(), t, s, "PLfixture", []testMember{{"a", 1}, {"b", 2}}, true); err != nil {
		t.Fatal(err)
	}
	first, err := s.Episodes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = reconcileMembers(t.Context(), t, s, "PLfixture", []testMember{{"a", 1}, {"b", 2}}, true); err != nil {
		t.Fatal(err)
	}
	same, err := s.Episodes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Updated != same[0].Updated {
		t.Fatal("unchanged playlist advanced time")
	}
	if err = reconcileMembers(t.Context(), t, s, "PLfixture", []testMember{{"b", 1}, {"a", 2}}, true); err != nil {
		t.Fatal(err)
	}
	reordered, err := s.Episodes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if reordered[0].VideoID != "b" || reordered[0].Updated <= first[1].Updated || reordered[1].Updated <= first[0].Updated {
		t.Fatal("reorder left stale occurrence times", first, reordered)
	}
	if err = s.SavePlaylist(t.Context(), Playlist{ID: "PLfixture", ChannelID: "owner", Title: "Renamed"}); err != nil {
		t.Fatal(err)
	}
	renamed, err := s.Episodes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if renamed[0].SeasonTitle != "Renamed" || renamed[0].Updated <= reordered[0].Updated {
		t.Fatal("rename left stale time", renamed)
	}
}

func TestProjectionPagesUseVisibilityAndExactNames(t *testing.T) {
	s := collectionStore(t)
	preparedVideo(t, s, "a", "A")
	if err := s.ExposePrepared(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	visible := Visibility{Channels: []string{"uploader"}}
	page, err := s.ProjectionFilesPage(t.Context(), visible, "uploader", 2026, "", 1)
	if err != nil || len(page) != 1 || page[0].Size != 4096 {
		t.Fatal(page, err)
	}
	seasons, err := s.ProjectionSeasons(t.Context(), visible, "uploader", "", 1)
	if err != nil || len(seasons) != 1 || seasons[0].Scope != "year:2026" {
		t.Fatal(seasons, err)
	}
	posters, err := s.ProjectionPosters(t.Context(), visible, "uploader", "", 1)
	if err != nil || len(posters) != 1 || posters[0].Name != "season2026-poster.jpg" {
		t.Fatal(posters, err)
	}
	if _, err = s.ProjectionChanged(t.Context(), Visibility{}, "uploader"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("unfollowed projection visible", err)
	}
	for _, scope := range []string{"year:2026", "playlist:absent"} {
		exists, err := s.HasSeason(t.Context(), "uploader", scope)
		if err != nil || exists != (scope == "year:2026") {
			t.Fatal(scope, exists, err)
		}
	}
	for _, limit := range []int{0, 257} {
		if _, err = s.ProjectionPage(t.Context(), visible, "uploader", 2026, 0, limit); err == nil {
			t.Fatal("unbounded episode page")
		}
		if _, err = s.ProjectionFilesPage(t.Context(), visible, "uploader", 2026, "", limit); err == nil {
			t.Fatal("unbounded file page")
		}
		if _, err = s.ProjectionSeasons(t.Context(), visible, "uploader", "", limit); err == nil {
			t.Fatal("unbounded season page")
		}
		if _, err = s.ProjectionPosters(t.Context(), visible, "uploader", "", limit); err == nil {
			t.Fatal("unbounded poster page")
		}
	}
}

func TestProjectionMigrationPreservesExistingBindings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "catalogue.sqlite")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureChannel(t.Context(), "owner", "Owner"); err != nil {
		t.Fatal(err)
	}
	m := source.Metadata{ID: "a", ChannelID: "owner", Title: "A", UploadDate: "20261025", Duration: 60}
	if err = s.SaveMetadata(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err = s.SavePrepared(t.Context(), Prepared{Metadata: m, Revision: "before", Height: 2160, Size: 4096}); err != nil {
		t.Fatal(err)
	}
	if err = s.BindOccurrence(t.Context(), "youtube-video:a", "server", "42", "guid"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`DROP INDEX video_projection`, `ALTER TABLE videos DROP COLUMN requested_height`, `ALTER TABLE playlist_members DROP COLUMN updated`, `ALTER TABLE playlists DROP COLUMN updated`, `ALTER TABLE channels DROP COLUMN updated`, `PRAGMA user_version=2`} {
		if _, err = s.DB.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestStore(t, s)
	var key string
	if err = s.DB.QueryRowContext(t.Context(), `SELECT rating_key FROM episode_bindings`).Scan(&key); err != nil || key != "42" {
		t.Fatal(key, err)
	}
	ready, err := s.PreparedForHeight(t.Context(), "a", 2160)
	if err != nil || !ready {
		t.Fatal("migrated verified representation lost", ready, err)
	}
}

func closeTestStore(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Error(err)
	}
}
