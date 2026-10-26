package catalog

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProjectionTimesFollowEveryVisibilityAndTimestampTransition(t *testing.T) {
	s := collectionStore(t)
	for _, id := range []string{"a", "b", "c"} {
		preparedVideo(t, s, id, id)

		if err := s.ExposePrepared(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}

	for _, id := range []string{"PLfirst", "PLsecond"} {
		if err := s.SavePlaylist(t.Context(), Playlist{ID: id, ChannelID: "owner", Title: id}); err != nil {
			t.Fatal(err)
		}

		if err := reconcileMembers(t.Context(), t, s, id, []testMember{{"a", 1}, {"b", 2}}, true); err != nil {
			t.Fatal(err)
		}
	}

	for _, query := range []string{
		`UPDATE videos SET updated=100 WHERE id='a'`,
		`UPDATE videos SET updated=200 WHERE id='b'`,
		`UPDATE channels SET updated=0`,
		`UPDATE playlists SET updated=0`,
		`UPDATE playlist_members SET updated=0`,
		`UPDATE playlist_members SET updated=300 WHERE playlist_id='PLfirst' AND video_id='a'`,
		`UPDATE videos SET state='unavailable' WHERE id='a'`,
		`UPDATE videos SET revision='' WHERE id='b'`,
		`UPDATE videos SET state='replay-ready' WHERE id='a'`,
		`UPDATE videos SET revision='returned' WHERE id='b'`,
		`UPDATE playlist_members SET selected=0 WHERE playlist_id='PLfirst' AND video_id='a'`,
		`UPDATE playlist_members SET selected=1,episode=0 WHERE playlist_id='PLfirst' AND video_id='a'`,
		`UPDATE playlist_members SET episode=2,updated=400 WHERE playlist_id='PLfirst' AND video_id='a'`,
		`UPDATE playlist_members SET selected=0,episode=0 WHERE playlist_id='PLsecond'`,
		`UPDATE playlists SET title='Renamed',updated=500 WHERE id='PLfirst'`,
		`UPDATE channels SET title='Owner renamed',updated=600 WHERE id='owner'`,
		`UPDATE videos SET selected=0 WHERE id='b'`,
		`UPDATE videos SET selected=1,year=2025 WHERE id='b'`,
		`UPDATE videos SET number=0 WHERE id='b'`,
		`UPDATE videos SET number=1 WHERE id='b'`,
		`DELETE FROM playlist_members WHERE playlist_id='PLfirst' AND video_id='a'`,
		`INSERT INTO playlist_members(playlist_id,video_id,position,selected,episode,file_episode,updated) VALUES('PLfirst','a',1,1,3,3,700)`,
		`UPDATE videos SET updated=50 WHERE id='a'`,
		`UPDATE playlist_members SET updated=40 WHERE video_id='a'`,
		`UPDATE videos SET state='upcoming' WHERE id='b'`,
		`UPDATE videos SET state='recorded',updated=800 WHERE id='b'`,
		`UPDATE videos SET channel_id='owner',year=2024 WHERE id='c'`,
		`UPDATE playlist_members SET video_id='c' WHERE playlist_id='PLfirst' AND video_id='a'`,
		`UPDATE playlist_members SET playlist_id='PLsecond' WHERE video_id='c'`,
		`DELETE FROM playlist_members WHERE video_id='c'`,
		`DELETE FROM metadata_revisions WHERE video_id='c'`,
		`DELETE FROM preparation_jobs WHERE video_id='c'`,
		`DELETE FROM asset_jobs WHERE video_id='c'`,
		`DELETE FROM discovery_items WHERE video_id='c'`,
		`DELETE FROM videos WHERE id='c'`,
	} {
		if _, err := s.DB.ExecContext(t.Context(), query); err != nil {
			t.Fatal(query, err)
		}

		checkProjectionTimes(t, s)
	}

	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = tx.ExecContext(t.Context(), `UPDATE videos SET state='unavailable'; DELETE FROM playlist_members`); err != nil {
		t.Fatal(err)
	}

	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	checkProjectionTimes(t, s)
}

func checkProjectionTimes(t *testing.T, s *Store) {
	t.Helper()

	for _, visible := range []Visibility{
		{},
		{Channels: []string{"uploader", "owner"}},
		{PlaylistChannels: []string{"owner"}},
		{Channels: []string{"uploader", "owner"}, PlaylistChannels: []string{"owner"}, SkippedPlaylists: []string{"PLfirst"}},
		{Channels: []string{"uploader", "owner"}, PlaylistChannels: []string{"owner"}, SkippedPlaylists: []string{"PLsecond"}},
	} {
		for _, owner := range []string{"uploader", "owner", "missing"} {
			cte, args := projectionCTE(visible)
			args = append(args, owner)

			rows, err := s.DB.QueryContext(t.Context(), cte+`SELECT printf('Season %04d',season),CASE WHEN playlist='' THEN 'year:'||season ELSE 'playlist:'||playlist END,season_title,season,MAX(updated) FROM episodes WHERE owner=? GROUP BY season ORDER BY printf('Season %04d',season)`, args...)
			if err != nil {
				t.Fatal(err)
			}

			want, err := readProjectionSeasons(rows)
			if err != nil {
				t.Fatal(err)
			}

			got, err := s.ProjectionSeasons(t.Context(), visible, owner, "", 256)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("season metadata differs from episode aggregation", owner, visible, got, want, err)
			}

			var latest int64
			for _, season := range want {
				latest = max(latest, season.Modified)

				found, err := s.ProjectionSeasonAt(t.Context(), visible, owner, season.Number)
				if err != nil || found != season {
					t.Fatal("single-season metadata differs", found, season, err)
				}
			}

			changed, err := s.ProjectionChanged(t.Context(), visible, owner)
			if len(want) == 0 {
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatal("empty projection became visible", owner, visible, changed, err)
				}
			} else if err != nil || changed != latest {
				t.Fatal("show timestamp differs", owner, visible, changed, latest, err)
			}
		}
	}
}

func TestProjectionTimeMigrationBackfillsAndPreservesMedia(t *testing.T) {
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

	prepared := preparedVideo(t, s, "a", "Original")
	if err = s.ExposePrepared(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}

	if err = s.SavePlaylist(t.Context(), Playlist{ID: "PLfirst", ChannelID: "owner", Title: "First"}); err != nil {
		t.Fatal(err)
	}

	if err = reconcileMembers(t.Context(), t, s, "PLfirst", []testMember{{"a", 1}}, true); err != nil {
		t.Fatal(err)
	}

	if err = s.BindOccurrence(t.Context(), PlaylistOccurrenceID("owner", "PLfirst", "a"), "server", "42", "guid"); err != nil {
		t.Fatal(err)
	}

	removeSchema6(t, s)

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestStore(t, s)

	checkProjectionTimes(t, s)

	video, err := s.Video(t.Context(), "a")
	if err != nil || video.Revision != prepared.Revision {
		t.Fatal("metadata migration changed media", video, err)
	}

	var binding string
	if err = s.DB.QueryRowContext(t.Context(), `SELECT rating_key FROM episode_bindings`).Scan(&binding); err != nil || binding != "42" {
		t.Fatal(binding, err)
	}

	var version int
	if err = s.DB.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version); err != nil || version != 6 {
		t.Fatal(version, err)
	}
}

func removeSchema6(t *testing.T, s *Store) {
	t.Helper()

	for _, query := range []string{
		`DROP TRIGGER projection_year_insert`, `DROP TRIGGER projection_year_update`,
		`DROP TRIGGER projection_time_insert`, `DROP TRIGGER projection_time_update`, `DROP TRIGGER projection_time_delete`, `DROP TRIGGER projection_time_media`,
		`DROP TABLE projection_years`, `DROP TABLE projection_playlist_times`, `DROP INDEX video_projection_time`,
		`PRAGMA user_version=5`,
	} {
		if _, err := s.DB.ExecContext(t.Context(), query); err != nil {
			t.Fatal(query, err)
		}
	}
}

func TestProjectionTimeQueryUsesIndexedMaxima(t *testing.T) {
	s := collectionStore(t)
	cte, args := projectionSeasonsCTE(Visibility{Channels: []string{"uploader"}, PlaylistChannels: []string{"uploader"}}, "uploader")

	rows, err := s.DB.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+cte+`SELECT MAX(modified) FROM seasons`, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRows(rows)

	plan := ""

	for rows.Next() {
		var (
			id, parent, unused int
			detail             string
		)
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}

		plan += detail + "\n"
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(plan, "video_projection_time") || !strings.Contains(plan, "projection_playlist_latest") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatal("directory timestamps lost their indexed maxima", plan)
	}
}
