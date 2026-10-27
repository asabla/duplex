package catalog

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestPlaylistScannerSlotsSurviveOrderRemovalAndReturn(t *testing.T) {
	s := collectionStore(t)
	for _, id := range []string{"a", "b", "c", "d"} {
		preparedVideo(t, s, id, id)
	}

	if err := s.SavePlaylist(t.Context(), Playlist{ID: "PLfirst", ChannelID: "owner", Title: "First"}); err != nil {
		t.Fatal(err)
	}

	visible := Visibility{PlaylistChannels: []string{"owner"}}

	if err := reconcileMembers(t.Context(), t, s, "PLfirst", []testMember{{"a", 1}, {"b", 2}, {"c", 3}}, true); err != nil {
		t.Fatal(err)
	}

	before, err := s.Episodes(t.Context())
	if err != nil || len(before) != 3 {
		t.Fatal(before, err)
	}

	for _, members := range [][]testMember{{{"c", 1}, {"a", 2}, {"d", 3}}, {{"d", 1}, {"b", 2}, {"a", 3}, {"c", 4}}} {
		if err = reconcileMembers(t.Context(), t, s, "PLfirst", members, true); err != nil {
			t.Fatal(err)
		}

		for _, old := range before {
			var selected bool
			for _, m := range members {
				selected = selected || old.VideoID == m.id
			}

			got, err := s.ProjectionFile(t.Context(), visible, "owner", 1, old.FileNumber)
			if !selected {
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatal("removed path was reused", got, err)
				}

				continue
			}

			if err != nil || got.VideoID != old.VideoID || got.FileNumber != old.FileNumber {
				t.Fatal("scanner slot changed", old, got, err)
			}

			index, err := s.PlaylistOrder(t.Context(), visible, got.OccurrenceID)
			if err != nil || index != got.Number {
				t.Fatal(index, got, err)
			}
		}
	}

	for _, id := range []string{"youtube-video:a", "youtube-playlist:other:PLfirst:a", "youtube-playlist:owner:PLmissing:a", "youtube-playlist:owner:PLfirst:missing"} {
		if _, err := s.PlaylistOrder(t.Context(), visible, id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("unowned occurrence acquired an order", id, err)
		}
	}

	if _, err = s.PlaylistOrder(t.Context(), Visibility{}, before[0].OccurrenceID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("disabled playlist acquired an order", err)
	}
}

func TestPlaylistPathMigrationPreservesExposedPathsAndBindings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "catalogue.sqlite")

	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.EnsureChannel(t.Context(), "uploader", "Uploader"); err != nil {
		t.Fatal(err)
	}

	if err = s.EnsureChannel(t.Context(), "owner", "Owner"); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"a", "b", "c"} {
		preparedVideo(t, s, id, id)
	}

	if err = s.SavePlaylist(t.Context(), Playlist{ID: "PLfirst", ChannelID: "owner", Title: "First"}); err != nil {
		t.Fatal(err)
	}

	if err = reconcileMembers(t.Context(), t, s, "PLfirst", []testMember{{"a", 1}, {"b", 2}, {"c", 3}}, true); err != nil {
		t.Fatal(err)
	}

	if err = s.BindOccurrence(t.Context(), PlaylistOccurrenceID("owner", "PLfirst", "b"), "server", "42", "guid"); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{
		`UPDATE playlist_members SET selected=0 WHERE video_id='a'`,
		`UPDATE playlist_members SET episode=1 WHERE video_id='b'`,
	} {
		if _, err = s.DB.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}

	removeSchema5(t, s)

	if _, err = s.DB.ExecContext(t.Context(), `PRAGMA user_version=4`); err != nil {
		t.Fatal(err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestStore(t, s)

	visible := Visibility{PlaylistChannels: []string{"owner"}}

	ep, err := s.ProjectionFile(t.Context(), visible, "owner", 1, 1)
	if err != nil || ep.VideoID != "b" || ep.Number != 1 {
		t.Fatal("migration changed an exposed scanner path", ep, err)
	}

	var binding string
	if err = s.DB.QueryRowContext(t.Context(), `SELECT rating_key FROM episode_bindings`).Scan(&binding); err != nil || binding != "42" {
		t.Fatal(binding, err)
	}

	var oldSlot int
	if err = s.DB.QueryRowContext(t.Context(), `SELECT file_episode FROM playlist_members WHERE video_id='a'`).Scan(&oldSlot); err != nil || oldSlot <= 3 {
		t.Fatal("historical slot conflicts with active media", oldSlot, err)
	}
}

func removeSchema5(t *testing.T, s *Store) {
	t.Helper()

	if _, err := s.DB.ExecContext(t.Context(), `DROP INDEX episode_video_bindings`); err != nil {
		t.Fatal(err)
	}

	removeSchema6(t, s)

	for _, query := range []string{
		`DROP INDEX playlist_projection_name`, `DROP INDEX video_projection_name`,
		`DROP TRIGGER playlist_hint_insert`, `DROP TRIGGER playlist_hint_update`, `DROP TRIGGER playlist_hint_delete`, `DROP TRIGGER playlist_hint_media`,
		`DROP INDEX observed_episodes`,
		`ALTER TABLE episode_bindings DROP COLUMN owner_id`, `ALTER TABLE episode_bindings DROP COLUMN playlist_id`, `ALTER TABLE episode_bindings DROP COLUMN video_id`, `ALTER TABLE episode_bindings DROP COLUMN current_index`, `ALTER TABLE episode_bindings DROP COLUMN seen_run`,
		`DROP TABLE catalogue_identity`, `ALTER TABLE playlists DROP COLUMN projection_revision`,
		`DROP INDEX playlist_episode_numbers`, `DROP INDEX playlist_video_membership`,
		`DROP TABLE show_metadata`, `DROP INDEX playlist_file_slots`, `ALTER TABLE playlist_members DROP COLUMN file_episode`,
	} {
		if _, err := s.DB.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
}
