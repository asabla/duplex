package catalog

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestEpisodeAssetsKeepVersionsRetriesAndAllOccurrences(t *testing.T) {
	s := collectionStore(t)
	preparedVideo(t, s, "fixture", "Fixture")

	if changed, err := s.RecordEpisodeAssets(t.Context(), "fixture", "first"); err != nil || !changed {
		t.Fatal(changed, err)
	}

	if _, err := s.NextEpisodeRefresh(t.Context(), "server"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("unscanned episode was reserved", err)
	}

	items := []ObservedEpisode{{Occurrence: "youtube-video:fixture", Key: "42", GUID: "upload-guid", Index: 1}, {Occurrence: "youtube-playlist:owner:PLfixture:fixture", Key: "43", GUID: "playlist-guid", Index: 2}}
	if err := s.ObserveEpisodes(t.Context(), "server", 1, items); err != nil {
		t.Fatal(err)
	}

	job, err := s.NextEpisodeRefresh(t.Context(), "server")
	if err != nil || job.Video != "fixture" || job.Digest != "first" {
		t.Fatal(job, err)
	}

	if _, err = s.NextEpisodeRefresh(t.Context(), "server"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("reserved work was duplicated", err)
	}

	after, count := "", 0
	for {
		page, err := s.EpisodeRefreshPage(t.Context(), "server", "fixture", after, 1)
		if err != nil {
			t.Fatal(err)
		}

		if len(page) == 0 {
			break
		}

		count++
		after = page[0].Occurrence
	}

	if count != 2 {
		t.Fatal("shared video occurrences were lost", count)
	}

	if err = s.AppliedEpisodeAssets(t.Context(), job, "server", items[0]); err != nil {
		t.Fatal(err)
	}

	remaining, err := s.EpisodeRefreshPage(t.Context(), "server", "fixture", "", 128)
	if err != nil || len(remaining) != 1 || remaining[0].Key != "43" {
		t.Fatal("partial completion was not checkpointed", remaining, err)
	}

	if changed, err := s.RecordEpisodeAssets(t.Context(), "fixture", "second"); err != nil || !changed {
		t.Fatal(changed, err)
	}

	if err = s.AppliedEpisodeAssets(t.Context(), job, "server", items[1]); err != nil {
		t.Fatal(err)
	}

	remaining, err = s.EpisodeRefreshPage(t.Context(), "server", "fixture", "", 128)
	if err != nil || len(remaining) != 2 {
		t.Fatal("old completion acknowledged new assets", remaining, err)
	}

	if err = s.FinishEpisodeRefresh(t.Context(), job, nil); err != nil {
		t.Fatal(err)
	}

	job, err = s.NextEpisodeRefresh(t.Context(), "server")
	if err != nil || job.Digest != "second" {
		t.Fatal("old completion lost new assets", job, err)
	}

	if err = s.FinishEpisodeRefresh(t.Context(), job, errors.New("private locator")); err != nil {
		t.Fatal(err)
	}

	if changed, err := s.RecordEpisodeAssets(t.Context(), "fixture", "second"); err != nil || changed {
		t.Fatal("unchanged assets requeued a failed job", changed, err)
	}

	if pending, message, err := s.EpisodeRefreshStatus(t.Context()); err != nil || pending != 1 || message != "Plex episode metadata refresh failed; retry is delayed" {
		t.Fatal(pending, message, err)
	}

	if _, err = s.NextEpisodeRefresh(t.Context(), "server"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("failed work retried immediately", err)
	}

	if _, err = s.DB.ExecContext(t.Context(), `UPDATE episode_metadata SET attempted=0`); err != nil {
		t.Fatal(err)
	}

	if _, err = s.NextEpisodeRefresh(t.Context(), "server"); err != nil {
		t.Fatal(err)
	}

	if err = s.FinishEpisodeRefresh(t.Context(), job, nil); err != nil {
		t.Fatal(err)
	}

	if pending, _, err := s.EpisodeRefreshStatus(t.Context()); err != nil || pending != 0 {
		t.Fatal(pending, err)
	}

	if err = s.ObserveEpisodes(t.Context(), "server", 2, items[:1]); err != nil {
		t.Fatal(err)
	}

	page, err := s.EpisodeRefreshPage(t.Context(), "server", "fixture", "", 128)
	if err != nil || len(page) != 1 || page[0].Key != "42" {
		t.Fatal("stale occurrence returned", page, err)
	}

	if _, err = s.EpisodeRefreshPage(t.Context(), "server", "fixture", "", 129); err == nil {
		t.Fatal("unbounded page accepted")
	}

	if _, err = s.RecordEpisodeAssets(t.Context(), "fixture", ""); err == nil {
		t.Fatal("empty digest accepted")
	}
}

func TestEpisodeRefreshResumesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalogue.sqlite")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeTestStore(t, s) })

	if err = s.EnsureChannel(t.Context(), "uploader", "Uploader"); err != nil {
		t.Fatal(err)
	}

	preparedVideo(t, s, "fixture", "Fixture")

	if _, err = s.RecordEpisodeAssets(t.Context(), "fixture", "digest"); err != nil {
		t.Fatal(err)
	}

	if err = s.ObserveEpisodes(t.Context(), "server", 1, []ObservedEpisode{{Occurrence: "youtube-video:fixture", Key: "42", Index: 1}}); err != nil {
		t.Fatal(err)
	}

	job, err := s.NextEpisodeRefresh(t.Context(), "server")
	if err != nil {
		t.Fatal(err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}

	resumed, err := s.NextEpisodeRefresh(t.Context(), "server")
	if err != nil || resumed != job {
		t.Fatal("interrupted refresh was not resumed", resumed, err)
	}

	if err = s.FinishEpisodeRefresh(t.Context(), resumed, errors.New("controlled failure")); err != nil {
		t.Fatal(err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.NextEpisodeRefresh(t.Context(), "server"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("restart bypassed failure backoff", err)
	}
}
