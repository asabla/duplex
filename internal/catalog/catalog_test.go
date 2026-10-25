package catalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"duplex/internal/source"
)

func TestSnapshotIdentityWindowAndRollback(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "catalogue.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	ch := source.Channel{ID: "channel", Title: "Channel", Entries: []source.Entry{{ID: "first"}, {ID: "second"}}}
	prep := func(id, date, title string) Prepared {
		return Prepared{Metadata: source.Metadata{ID: id, ChannelID: ch.ID, Title: title, UploadDate: date, LiveStatus: "not_live", Duration: 30}, Revision: id + "-revision", Size: 100, Height: 1080}
	}
	if e = s.CommitSnapshot(ctx, ch, []Prepared{prep("first", "20260102", "First"), prep("second", "20260103", "Second")}, 1); e != nil {
		t.Fatal(e)
	}
	rows, _ := s.Videos(ctx)
	numbers := map[string]int{}
	for _, v := range rows {
		numbers[v.ID] = v.Number
		if v.ID == "second" && !v.Exposed || v.ID == "first" && v.Exposed {
			t.Fatal("window not applied after complete snapshot", rows)
		}
	}
	ch.Entries = append(ch.Entries, source.Entry{ID: "older"})
	if e = s.CommitSnapshot(ctx, ch, []Prepared{prep("first", "20260102", "Renamed"), prep("older", "20260101", "Backfill")}, 2); e != nil {
		t.Fatal(e)
	}
	rows, _ = s.Videos(ctx)
	for _, v := range rows {
		if v.ID == "second" && v.Exposed {
			t.Fatal("unprepared historical revision re-entered current window", v)
		}
		if old, ok := numbers[v.ID]; ok && v.Number != old {
			t.Fatal("renumbered", v)
		}
	}
	bad := prep("first", "20260102", "Broken")
	bad.Metadata.ChannelID = "different"
	if e = s.CommitSnapshot(ctx, source.Channel{ID: ch.ID, Title: "Changed", Entries: []source.Entry{{ID: "first"}}}, []Prepared{bad}, 1); e == nil {
		t.Fatal("bad partial snapshot committed")
	}
	after, _ := s.Videos(ctx)
	if len(after) != 3 {
		t.Fatal("identity records removed")
	}
	for _, v := range after {
		if v.ID == "first" && v.Title != "Renamed" {
			t.Fatal("rollback lost prior snapshot")
		}
	}
	ch.Entries = []source.Entry{{ID: "older"}}
	if e = s.CommitSnapshot(ctx, ch, nil, 1); e != nil {
		t.Fatal(e)
	}
	rows, _ = s.Videos(ctx)
	for _, v := range rows {
		if v.ID == "first" && (v.Number != numbers[v.ID] || v.Exposed) {
			t.Fatal("removed identity was lost or exposed")
		}
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
	defer live.Close()
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
	defer backup.Close()
	output := filepath.Join(dir, "backup.sqlite")
	if e = backup.Backup(ctx, output); e != nil {
		t.Fatal(e)
	}
	jobs, e := live.Jobs(ctx)
	if e != nil || len(jobs) != 1 || jobs[0].State != "running" {
		t.Fatal(jobs, e)
	}
	st, e := os.Stat(output)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal(st, e)
	}
	if e = backup.Backup(ctx, output); e == nil {
		t.Fatal("overwrote existing backup")
	}
	copy, e := OpenForBackup(output)
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	var result string
	if e = copy.DB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); e != nil || result != "ok" {
		t.Fatal(result, e)
	}
	copied, e := copy.Jobs(ctx)
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
	defer store.Close()
	ch := source.Channel{ID: "channel", Title: "Channel", Entries: []source.Entry{{ID: "recording"}}}
	for _, live := range []string{"is_upcoming", "is_live", "post_live", "was_live"} {
		m := source.Metadata{ID: "recording", ChannelID: "channel", Title: "Broadcast", LiveStatus: live, Duration: 60, UploadDate: "20260102"}
		p := Prepared{Metadata: m}
		if live == "was_live" {
			p.Revision = "verified-replay"
			p.Height = 1080
			p.Size = 1024
		}
		if e = store.CommitSnapshot(ctx, ch, []Prepared{p}, 50); e != nil {
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
