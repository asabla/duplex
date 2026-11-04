package catalog

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestEpisodeAssetsAdvanceEveryOwnerDirectoryOnlyWhenChanged(t *testing.T) {
	s := collectionStore(t)
	preparedVideo(t, s, "fixture", "Fixture")

	if err := s.ExposePrepared(t.Context(), "fixture"); err != nil {
		t.Fatal(err)
	}

	for _, owner := range []string{"other", "unrelated"} {
		if err := s.EnsureChannel(t.Context(), owner, owner); err != nil {
			t.Fatal(err)
		}
	}

	for id, owner := range map[string]string{"PLfirst": "owner", "PLsecond": "other"} {
		if err := s.SavePlaylist(t.Context(), Playlist{ID: id, ChannelID: owner, Title: id}); err != nil {
			t.Fatal(err)
		}

		if err := s.AddPlaylistMember(t.Context(), id, "fixture", 1, true); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.DB.ExecContext(t.Context(), `UPDATE channels SET updated=?`, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	visible := Visibility{Channels: []string{"uploader"}, PlaylistChannels: []string{"owner", "other"}}
	directories := func() map[string]int64 {
		t.Helper()

		result := map[string]int64{}

		for _, owner := range []string{"uploader", "owner", "other"} {
			seasons, err := s.ProjectionSeasons(t.Context(), visible, owner, "", 128)
			if err != nil || len(seasons) != 1 {
				t.Fatal(owner, seasons, err)
			}

			result[fmt.Sprintf("%s/%s", owner, seasons[0].Name)] = seasons[0].Modified
		}

		return result
	}
	before := directories()

	videoBefore, err := s.Video(t.Context(), "fixture")
	if err != nil {
		t.Fatal(err)
	}

	var unrelated int64
	if err = s.DB.QueryRowContext(t.Context(), `SELECT updated FROM channels WHERE id='unrelated'`).Scan(&unrelated); err != nil {
		t.Fatal(err)
	}

	if changed, err := s.RecordEpisodeAssets(t.Context(), "fixture", "first"); err != nil || !changed {
		t.Fatal(changed, err)
	}

	after := directories()
	for name, modified := range before {
		if after[name] <= modified {
			t.Fatal("new sidecar retained a stale NFS directory time", name, modified, after[name])
		}
	}

	videoAfter, err := s.Video(t.Context(), "fixture")
	if err != nil || !reflect.DeepEqual(videoBefore, videoAfter) {
		t.Fatal("asset publication changed recording identity or metadata", videoBefore, videoAfter, err)
	}

	var stillUnrelated int64
	if err = s.DB.QueryRowContext(t.Context(), `SELECT updated FROM channels WHERE id='unrelated'`).Scan(&stillUnrelated); err != nil || stillUnrelated != unrelated {
		t.Fatal("unrelated owner was touched", err)
	}

	if changed, err := s.RecordEpisodeAssets(t.Context(), "fixture", "first"); err != nil || changed || !reflect.DeepEqual(directories(), after) {
		t.Fatal("unchanged assets invalidated directory caches", changed, err)
	}

	if _, err = s.DB.ExecContext(t.Context(), `CREATE TRIGGER reject_asset_directory BEFORE UPDATE ON channels BEGIN SELECT RAISE(FAIL,'controlled directory update failure'); END`); err != nil {
		t.Fatal(err)
	}

	if _, err = s.RecordEpisodeAssets(t.Context(), "fixture", "second"); err == nil {
		t.Fatal("directory failure was ignored")
	}

	var digest string
	if err = s.DB.QueryRowContext(t.Context(), `SELECT digest FROM episode_metadata WHERE video_id='fixture'`).Scan(&digest); err != nil || digest != "first" {
		t.Fatal("failed directory publication committed its digest", digest, err)
	}
}
