package catalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"duplex/internal/source"
)

func TestStableIdentityAcrossSelectionTitleAndBackfill(t *testing.T) {
	s := collectionStore(t)
	ctx := t.Context()
	makeVideo := func(id, date, title string) Prepared {
		return Prepared{Metadata: source.Metadata{ID: id, ChannelID: "owner", Channel: "Owner", Title: title, UploadDate: date, Duration: 60}, Revision: id + "-revision", Height: 1080, Size: 4096}
	}
	first, second := makeVideo("first", "20260102", "First"), makeVideo("second", "20260103", "Second")
	for _, p := range []Prepared{first, second} {
		if err := s.SaveMetadata(ctx, p.Metadata); err != nil {
			t.Fatal(err)
		}
		if err := s.SavePrepared(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SelectUploads(ctx, "owner", []string{"second"}); err != nil {
		t.Fatal(err)
	}
	original, err := s.Video(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	first.Metadata.Title = "Renamed"
	if err = s.SaveMetadata(ctx, first.Metadata); err != nil {
		t.Fatal(err)
	}
	older := makeVideo("older", "20260101", "Backfill")
	if err = s.SaveMetadata(ctx, older.Metadata); err != nil {
		t.Fatal(err)
	}
	if err = s.SavePrepared(ctx, older); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartJob(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	for n, id := range []string{"second", "first", "older"} {
		if err = s.StageDiscovery(ctx, run, "uploads", id, n+1); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.ReconcileDiscovery(ctx, run, "uploads", "owner", 2); err != nil {
		t.Fatal(err)
	}
	current, err := s.Video(ctx, "first")
	if err != nil || current.Title != "Renamed" || current.Number != original.Number || current.Year != original.Year || !current.Exposed {
		t.Fatal(current, err)
	}
	backfill, err := s.Video(ctx, "older")
	if err != nil || backfill.Number <= original.Number || backfill.Exposed {
		t.Fatal(backfill, err)
	}
	if err = s.SelectUploads(ctx, "owner", []string{"older"}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.Video(ctx, "first")
	if err != nil || removed.Exposed || removed.Number != original.Number || removed.Revision != original.Revision {
		t.Fatal(removed, err)
	}
}

func TestOnlineBackupDoesNotInterruptJobs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "catalogue.sqlite")
	live, e := Open(file)
	if e != nil {
		t.Fatal(e)
	}
	defer closeTest(t, live)
	if e = live.EnsureChannel(ctx, "UCexample", "Example"); e != nil {
		t.Fatal(e)
	}
	if _, e = live.StartJob(ctx, "UCexample"); e != nil {
		t.Fatal(e)
	}
	backup, e := OpenForBackup(file)
	if e != nil {
		t.Fatal(e)
	}
	defer closeTest(t, backup)
	output := filepath.Join(dir, "backup.sqlite")
	if e = backup.Backup(ctx, output); e != nil {
		t.Fatal(e)
	}
	jobs, e := live.Jobs(ctx)
	if e != nil || len(jobs) != 1 || jobs[0].State != "running" {
		t.Fatal(jobs, e)
	}
	st, e := os.Stat(output)
	if e != nil || st.Mode().Perm() != 0o600 {
		t.Fatal(st, e)
	}
	if e = backup.Backup(ctx, output); e == nil {
		t.Fatal("overwrote existing backup")
	}
	restored, e := OpenForBackup(output)
	if e != nil {
		t.Fatal(e)
	}
	defer closeTest(t, restored)
	var result string
	if e = restored.DB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); e != nil || result != "ok" {
		t.Fatal(result, e)
	}
	copied, e := restored.Jobs(ctx)
	if e != nil || len(copied) != 1 || copied[0].State != "running" {
		t.Fatal(copied, e)
	}
}

func TestBroadcastTransitionPreservesSourceIdentity(t *testing.T) {
	ctx := context.Background()
	store, e := Open(filepath.Join(t.TempDir(), "catalogue.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer closeTest(t, store)
	ch := source.Channel{ID: "channel", Title: "Channel", Entries: []source.Entry{{ID: "recording"}}}
	for _, live := range []string{"is_upcoming", "is_live", "post_live", "was_live"} {
		m := source.Metadata{ID: "recording", ChannelID: "channel", Title: "Broadcast", LiveStatus: live, Duration: 60, UploadDate: "20260102"}
		p := Prepared{Metadata: m}
		if live == "was_live" {
			p.Revision = "verified-replay"
			p.Height = 1080
			p.Size = 1024
		}
		if e = store.SaveMetadata(ctx, m); e != nil {
			t.Fatal(e)
		}
		if p.Revision != "" {
			if e = store.SavePrepared(ctx, p); e != nil {
				t.Fatal(e)
			}
		}
		if e = store.SelectUploads(ctx, ch.ID, []string{m.ID}); e != nil {
			t.Fatal(e)
		}
		videos, e := store.Videos(ctx)
		if e != nil || len(videos) != 1 || videos[0].ID != "recording" {
			t.Fatal(videos, e)
		}
		if videos[0].Exposed != (live == "was_live") {
			t.Fatal("non-recorded media exposed", videos[0])
		}
	}
}
