package catalog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestProjectionEntriesKeepNamesCursorsAndPlaylistSlots(t *testing.T) {
	s := collectionStore(t)
	numbers := []int{1, 10, 9999, 10000, 10001}
	members := []testMember{}

	for i, number := range numbers {
		id := fmt.Sprintf("video%d", i)
		preparedVideo(t, s, id, id)

		if err := s.ExposePrepared(t.Context(), id); err != nil {
			t.Fatal(err)
		}

		if _, err := s.DB.ExecContext(t.Context(), `UPDATE videos SET number=? WHERE id=?`, number, id); err != nil {
			t.Fatal(err)
		}

		members = append(members, testMember{id, i + 1})
	}

	if err := s.SavePlaylist(t.Context(), Playlist{ID: "PLfixture", ChannelID: "owner", Title: "First"}); err != nil {
		t.Fatal(err)
	}

	if err := reconcileMembers(t.Context(), t, s, "PLfixture", members, true); err != nil {
		t.Fatal(err)
	}

	for i, number := range numbers {
		if _, err := s.DB.ExecContext(t.Context(), `UPDATE playlist_members SET file_episode=? WHERE video_id=?`, number, fmt.Sprintf("video%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	for i := range members {
		members[i].position = len(members) - i
	}

	if err := reconcileMembers(t.Context(), t, s, "PLfixture", members, true); err != nil {
		t.Fatal(err)
	}

	for _, scope := range []struct {
		owner, playlist string
		season          int
		visible         Visibility
	}{
		{"uploader", "", 2026, Visibility{Channels: []string{"uploader"}}},
		{"owner", "PLfixture", 1, Visibility{PlaylistChannels: []string{"owner"}}},
	} {
		prefix := fmt.Sprintf("Channel-%s - S%04dE", scope.owner, scope.season)
		for _, cursor := range []string{"", "A", "Z", prefix, prefix + "0001", prefix + "0001.nfo", prefix + "10000", prefix + "99999"} {
			for _, limit := range []int{1, 2, 5, 256} {
				want := []ProjectionEntry{}

				for i, number := range numbers {
					if fmt.Sprintf("%s%04d", prefix, number) >= cursor {
						want = append(want, ProjectionEntry{VideoID: fmt.Sprintf("video%d", i), PlaylistID: scope.playlist, FileNumber: number})
					}
				}

				sort.Slice(want, func(i, j int) bool {
					return fmt.Sprintf("%04d", want[i].FileNumber) < fmt.Sprintf("%04d", want[j].FileNumber)
				})
				want = want[:min(limit, len(want))]

				got, err := s.ProjectionEntries(t.Context(), scope.visible, scope.owner, scope.season, cursor, limit)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatal(scope.owner, cursor, limit, got, want, err)
				}
			}
		}
	}

	for _, visible := range []Visibility{{}, {PlaylistChannels: []string{"owner"}, SkippedPlaylists: []string{"PLfixture"}}} {
		entries, err := s.ProjectionEntries(t.Context(), visible, "owner", 1, "", 128)
		if err != nil || len(entries) != 0 {
			t.Fatal("hidden playlist exposed", entries, err)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := s.ProjectionEntries(ctx, Visibility{}, "owner", 1, "Z", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("empty-page shortcut ignored cancellation", err)
	}
}

func TestProjectionEntryPlansSeekWithoutTemporarySort(t *testing.T) {
	s := collectionStore(t)

	for _, scope := range []struct {
		owner, index string
		season       int
		visible      Visibility
	}{
		{"uploader", "video_projection_name", 2026, Visibility{Channels: []string{"uploader"}}},
		{"owner", "playlist_projection_name", 1, Visibility{PlaylistChannels: []string{"owner"}, SkippedPlaylists: []string{"other"}}},
	} {
		query, args := projectionEntriesQuery(scope.visible, scope.owner, scope.season, "10000", 128)

		rows, err := s.DB.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			if err := rows.Close(); err != nil {
				t.Error(err)
			}
		}()

		plan := ""

		for rows.Next() {
			var (
				id, parent, unused int
				detail             string
			)
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}

			plan += detail + "\n"
		}

		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(plan, scope.index) || strings.Contains(plan, "TEMP B-TREE") {
			t.Fatal("directory page lost its indexed ordering", plan)
		}
	}
}

func TestProjectionIndexesUpgradeWithoutChangingSchemaOrIdentities(t *testing.T) {
	file := filepath.Join(t.TempDir(), "catalogue.sqlite")

	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.EnsureChannel(t.Context(), "uploader", "Uploader"); err != nil {
		t.Fatal(err)
	}

	prepared := preparedVideo(t, s, "a", "Original")
	if _, err = s.DB.ExecContext(t.Context(), `DROP INDEX video_projection_name; DROP INDEX playlist_projection_name`); err != nil {
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

	var version, indexes int
	if err = s.DB.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version); err != nil || version != 5 {
		t.Fatal(version, err)
	}

	if err = s.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_schema WHERE type='index' AND name IN ('video_projection_name','playlist_projection_name')`).Scan(&indexes); err != nil || indexes != 2 {
		t.Fatal(indexes, err)
	}

	video, err := s.Video(t.Context(), "a")
	if err != nil || video.Revision != prepared.Revision {
		t.Fatal("read index upgrade changed media identity", video, err)
	}
}
