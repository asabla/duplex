package catalog

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestRestartWarmupIsBoundedDeduplicatedAndRespectsVisibility(t *testing.T) {
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

	if len(tasks) != 5 || tasks[0].VideoID != "video00" || tasks[0].Priority != 1000 {
		t.Fatal("warmup displaced requested work or exceeded its bound", tasks)
	}

	var warmed []string
	for _, task := range tasks[1:] {
		warmed = append(warmed, task.VideoID)
		if task.Priority != 100 {
			t.Fatal("warmup priority changed", task)
		}
	}

	if !reflect.DeepEqual(warmed, []string{"video05", "video06", "video07", "video08"}) {
		t.Fatal("warmup did not select unique recent ready recordings", warmed)
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
