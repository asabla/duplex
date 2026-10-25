package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/library"
	"duplex/internal/media"
	"duplex/internal/source"
)

const testOwner = "UCSMOQeBJ2RAnuFungnQOxLg"

type collectionSource struct {
	failingSource
	mu       sync.Mutex
	entries  map[string][]source.Entry
	metadata map[string]source.Metadata
	lists    []source.Playlist
	calls    []source.Scope
	resolved int
	fail     string
}

func (s *collectionSource) Entries(ctx context.Context, scope source.Scope, consume func([]source.Entry) error) (source.Listing, error) {
	key := scope.Tab
	if scope.PlaylistID != "" {
		key = scope.PlaylistID
	}
	s.mu.Lock()
	s.calls = append(s.calls, scope)
	entries := append([]source.Entry(nil), s.entries[key]...)
	s.mu.Unlock()
	start := min(max(0, scope.Start-1), len(entries))
	end := len(entries)
	if scope.Count > 0 {
		end = min(end, start+scope.Count)
	}
	for n := start; n < end; n += 25 {
		batch := append([]source.Entry(nil), entries[n:min(n+25, end)]...)
		for i := range batch {
			batch[i].Position = n + i + 1
		}
		if err := consume(batch); err != nil {
			return source.Listing{}, err
		}
	}
	if key == s.fail {
		return source.Listing{}, errors.New("interrupted enumeration")
	}
	return source.Listing{Count: end - start, Complete: end == len(entries)}, ctx.Err()
}

func (s *collectionSource) Playlists(context.Context, string) ([]source.Playlist, error) {
	return s.lists, nil
}

func (s *collectionSource) Metadata(ctx context.Context, id string) (source.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolved++
	m, ok := s.metadata[id]
	if !ok {
		return m, errors.New("missing metadata")
	}
	return m, ctx.Err()
}

func (s *collectionSource) Resolve(ctx context.Context, id string) (source.Metadata, error) {
	return s.Metadata(ctx, id)
}

func pipelineApp(t *testing.T, ch config.Channel, adapter source.SourceAdapter) *App {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := config.Create(file, filepath.Join(dir, "state")); err != nil {
		t.Fatal(err)
	}
	manager, err := config.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	snap := manager.Snapshot()
	snap.Config.Channels = []config.Channel{ch}
	if err = manager.Save(snap.Config, snap.Revision); err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(filepath.Join(dir, "catalogue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return &App{Config: manager, Catalog: db, Source: adapter, ctx: ctx, cancel: cancel, reps: map[string]media.Representation{}, assets: map[string][]library.Entry{}, leases: map[string]time.Time{}, prepareWake: make(chan struct{}, 2), scanWake: make(chan struct{}, 1)}
}

func pipelineChannel(policy config.Selection) config.Channel {
	return config.Channel{ID: testOwner, URL: "https://www.youtube.com/channel/" + testOwner, Enabled: true, Limit: 50, Indexing: &policy, Playlists: &config.PlaylistPolicy{Enabled: true}}
}

func fixtureCollection(n int) *collectionSource {
	s := &collectionSource{entries: map[string][]source.Entry{}, metadata: map[string]source.Metadata{}}
	for i := range n {
		id := fmt.Sprintf("video%06d", i)
		s.entries["videos"] = append(s.entries["videos"], source.Entry{ID: id, Title: id})
		s.metadata[id] = source.Metadata{ID: id, ChannelID: testOwner, Channel: "Channel", Title: id, Timestamp: float64(1700000000 - i*60), Duration: 60}
	}
	return s
}

func TestLatestDiscoveryDoesNotWaitForMedia(t *testing.T) {
	s := fixtureCollection(70)
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "latest", Limit: 3}), s)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	page, err := a.Videos(t.Context(), catalog.VideoFilter{Inclusion: "selected"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.Items[0].Preparation != "queued" || page.Items[0].Revision != "" {
		t.Fatal(page)
	}
	if s.resolved > 4 {
		t.Fatal("latest enumerated metadata outside its frontier", s.resolved)
	}
	for _, call := range s.calls {
		if call.Count > 3 {
			t.Fatal("unbounded Latest listing", call)
		}
	}
	n, err := a.Catalog.PendingPreparations(t.Context())
	if err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if err = a.PrepareNow(t.Context(), "video000002"); err != nil {
		t.Fatal(err)
	}
	id, err := a.Catalog.ClaimPreparation(t.Context())
	if err != nil || id != "video000002" {
		t.Fatal(id, err)
	}
	if err = a.PrepareNow(t.Context(), "outside"); err == nil {
		t.Fatal("outside selection queued")
	}
}

func TestAllAndPlaylistLatestOrdering(t *testing.T) {
	s := fixtureCollection(70)
	playlist := "PLfixture0001"
	s.lists = []source.Playlist{{ID: playlist, ChannelID: testOwner, Title: "Selected playlist"}}
	s.entries[playlist] = []source.Entry{{ID: "video000020"}, {ID: "video000002"}, {ID: "video000002"}, {ID: "video000001"}}
	ch := pipelineChannel(config.Selection{Mode: "all"})
	ch.Playlists.Overrides = map[string]config.Selection{playlist: {Mode: "latest", Limit: 2}}
	a := pipelineApp(t, ch, s)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	page, err := a.Videos(t.Context(), catalog.VideoFilter{Inclusion: "selected", PerPage: 100})
	if err != nil || page.Total != 70 {
		t.Fatal(page.Total, err)
	}
	rows, err := a.Catalog.DB.QueryContext(t.Context(), `SELECT video_id,episode FROM playlist_members WHERE playlist_id=? AND selected=1 ORDER BY episode`, playlist)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id string
		var n int
		if err = rows.Scan(&id, &n); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"video000002", "video000001"}) {
		t.Fatal(got)
	}
	// Reordering source membership must retain the occurrence identity and season.
	s.entries[playlist] = []source.Entry{{ID: "video000001"}, {ID: "video000002"}}
	if err = a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	playlists, err := a.Catalog.Playlists(t.Context(), testOwner)
	if err != nil || len(playlists) != 1 || playlists[0].Season != 1 {
		t.Fatal(playlists, err)
	}
}

func TestInterruptedPlaylistKeepsMembershipAndSkipsPolicy(t *testing.T) {
	s := fixtureCollection(4)
	id := "PLfixture0001"
	s.lists = []source.Playlist{{ID: id, ChannelID: testOwner, Title: "Playlist"}}
	s.entries[id] = []source.Entry{{ID: "video000001"}, {ID: "video000002"}}
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "latest", Limit: 2}), s)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	s.entries[id] = []source.Entry{{ID: "video000003"}}
	s.fail = id
	if err := a.Sync(t.Context(), testOwner); err == nil {
		t.Fatal("partial collection committed")
	}
	var old int
	if err := a.Catalog.DB.QueryRow(`SELECT count(*) FROM playlist_members WHERE playlist_id=? AND selected=1 AND video_id IN ('video000001','video000002')`, id).Scan(&old); err != nil || old != 2 {
		t.Fatal(old, err)
	}
	snap := a.Config.Snapshot()
	snap.Config.Channels[0].Playlists.Enabled = false
	if err := a.Config.Save(snap.Config, snap.Revision); err != nil {
		t.Fatal(err)
	}
	s.calls = nil
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal("skipped playlist enumerated", err)
	}
	for _, call := range s.calls {
		if call.PlaylistID != "" {
			t.Fatal(call)
		}
	}
}

func TestPreparationWaitingAndSelectionCancellation(t *testing.T) {
	s := fixtureCollection(1)
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "latest", Limit: 1}), s)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	delete(a.metadata, "video000000")
	a.mu.Unlock()
	m := s.metadata["video000000"]
	m.LiveStatus = "is_live"
	s.metadata[m.ID] = m
	if err := a.prepareRecording(t.Context(), m.ID); err != nil {
		t.Fatal(err)
	}
	v, err := a.Catalog.Video(t.Context(), m.ID)
	if err != nil || v.Preparation != "waiting" || v.State != "active" {
		t.Fatal(v, err)
	}
	snap := a.Config.Snapshot()
	snap.Config.Channels[0].Enabled = false
	if err = a.Config.Save(snap.Config, snap.Revision); err != nil {
		t.Fatal(err)
	}
	if err = a.prepareRecording(t.Context(), m.ID); err != nil {
		t.Fatal(err)
	}
	v, err = a.Catalog.Video(t.Context(), m.ID)
	if err != nil || v.Preparation != "skipped" {
		t.Fatal(v, err)
	}
}
