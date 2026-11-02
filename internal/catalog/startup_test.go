package catalog

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func TestRestartResumesRequestedWorkWithoutWarmingReadyRecordings(t *testing.T) {
	s := collectionStore(t)

	for n := range 10 {
		id := fmt.Sprintf("video%02d", n)
		preparedVideo(t, s, id, id)

		if _, err := s.DB.ExecContext(t.Context(), `UPDATE videos SET published=?,selected=1 WHERE id=?`, 100+n, id); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.SavePlaylist(t.Context(), Playlist{ID: "shared", ChannelID: "owner", Title: "Shared"}); err != nil {
		t.Fatal(err)
	}

	if err := s.AddPlaylistMember(t.Context(), "shared", "video07", 1, true); err != nil {
		t.Fatal(err)
	}

	if err := s.SetPreparation(t.Context(), "video09", "failed", "Controlled source failure"); err != nil {
		t.Fatal(err)
	}

	if err := s.QueuePreparation(t.Context(), "video00", 1000); err != nil {
		t.Fatal(err)
	}

	if err := s.QueuePreparation(t.Context(), "video01", 2000); err != nil {
		t.Fatal(err)
	}

	visible := Visibility{Channels: []string{"uploader"}, PlaylistChannels: []string{"owner"}}
	if err := s.ResetPreparations(t.Context(), visible); err != nil {
		t.Fatal(err)
	}

	var tasks []PreparationTask

	for {
		task, err := s.ClaimPreparationTask(t.Context())
		if errors.Is(err, sql.ErrNoRows) {
			break
		}

		if err != nil {
			t.Fatal(err)
		}

		tasks = append(tasks, task)
	}

	if len(tasks) != 2 || tasks[0].VideoID != "video00" || tasks[0].Priority != 1000 || tasks[1].VideoID != "video01" || tasks[1].Priority != 200 {
		t.Fatal("restart resolved ready recordings without a request", tasks)
	}

	if err := s.ResetPreparations(t.Context(), Visibility{}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ClaimPreparationTask(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("hidden recordings were requeued", err)
	}

	if err := s.ResetPreparations(t.Context(), Visibility{PlaylistChannels: []string{"owner"}, SkippedPlaylists: []string{"shared"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ClaimPreparationTask(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("skipped playlist was warmed", err)
	}
}
