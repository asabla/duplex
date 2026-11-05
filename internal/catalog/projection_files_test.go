package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestProjectionFilesMatchCanonicalProjectionAndSeekPlans(t *testing.T) {
	s := collectionStore(t)

	for i, number := range []int{1, 9999, 10000, 10001} {
		id := fmt.Sprintf("video%d", i)
		preparedVideo(t, s, id, id)

		if err := s.ExposePrepared(t.Context(), id); err != nil {
			t.Fatal(err)
		}

		if _, err := s.DB.ExecContext(t.Context(), `UPDATE videos SET number=? WHERE id=?`, number, id); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.SavePlaylist(t.Context(), Playlist{ID: "PLfixture", ChannelID: "owner", Title: "Playlist"}); err != nil {
		t.Fatal(err)
	}

	for i, number := range []int{1, 9999, 10000, 10001} {
		if err := s.AddPlaylistMember(t.Context(), "PLfixture", fmt.Sprintf("video%d", i), i+1, true); err != nil {
			t.Fatal(err)
		}

		if _, err := s.DB.ExecContext(t.Context(), `UPDATE playlist_members SET file_episode=? WHERE playlist_id='PLfixture' AND video_id=?`, number, fmt.Sprintf("video%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	for _, scope := range []struct {
		owner, index string
		season       int
		visible      Visibility
	}{
		{"uploader", "video_projection_name", 2026, Visibility{Channels: []string{"uploader"}}},
		{"owner", "playlist_projection_name", 1, Visibility{PlaylistChannels: []string{"owner"}}},
		{"owner", "", 1, Visibility{PlaylistChannels: []string{"owner"}, SkippedPlaylists: []string{"PLfixture"}}},
		{"uploader", "", 2026, Visibility{}},
	} {
		for _, number := range []int{1, 9999, 10000, 10001, 99999} {
			cte, args := projectionCTE(scope.visible)
			args = append(args, scope.owner, scope.season, number)

			rows, err := s.queryProjectionRows(t.Context(), cte+`SELECT owner,title,video,name,description,playlist,season_title,season,number,file_number,revision,size,updated FROM episodes WHERE owner=? AND season=? AND file_number=? LIMIT 1`, args...)
			if err != nil {
				t.Fatal(err)
			}

			want, err := readProjectionRows(rows)
			if err != nil {
				t.Fatal(err)
			}

			got, err := s.ProjectionFile(t.Context(), scope.visible, scope.owner, scope.season, number)

			media, mediaErr := s.ProjectionMedia(t.Context(), scope.visible, scope.owner, scope.season, number)
			if len(want) == 0 {
				if !errors.Is(err, sql.ErrNoRows) || !errors.Is(mediaErr, sql.ErrNoRows) {
					t.Fatal("hidden or missing slot exposed")
				}

				continue
			}

			if err != nil || mediaErr != nil || !reflect.DeepEqual(got, want[0]) || media.VideoID != got.VideoID || media.Revision != got.Revision || media.Size != got.Size || media.Updated != got.Updated {
				t.Fatal("direct lookup changed projection", got, want, err, mediaErr)
			}
		}

		page, err := s.ProjectionEntryDetails(t.Context(), scope.visible, scope.owner, scope.season, "", 128)
		if err != nil {
			t.Fatal(err)
		}

		plain, err := s.ProjectionEntries(t.Context(), scope.visible, scope.owner, scope.season, "", 128)
		if err != nil || len(page) != len(plain) {
			t.Fatal("detail page changed membership", err)
		}

		for i, ep := range page {
			if ep.VideoID != plain[i].VideoID || ep.FileNumber != plain[i].FileNumber {
				t.Fatal("detail page changed lexical order")
			}
		}

		if scope.index == "" {
			continue
		}

		query, args := projectionFilesQuery(scope.visible, scope.owner, scope.season, false, false, "10000", 128)

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
				a, b, c int
				detail  string
			)
			if err = rows.Scan(&a, &b, &c, &detail); err != nil {
				t.Fatal(err)
			}

			plan += detail + "\n"
		}

		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(plan, scope.index) || strings.Contains(plan, "TEMP B-TREE") {
			t.Fatal("detail page lost indexed order", plan)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := s.ProjectionEntryDetails(ctx, Visibility{}, "owner", 1, "Z", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("empty page hid cancellation", err)
	}
}
