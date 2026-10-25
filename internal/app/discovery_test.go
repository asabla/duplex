package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/media"
	"duplex/internal/source"
)

const testOwner = "UCSMOQeBJ2RAnuFungnQOxLg"

type collectionSource struct {
	failingSource
	mu          sync.Mutex
	entries     map[string][]source.Entry
	metadata    map[string]source.Metadata
	lists       []source.Playlist
	calls       []source.Scope
	resolved    int
	unavailable map[string]bool
	fail        string
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
	if s.unavailable[id] {
		return source.Metadata{}, source.ErrUnavailable
	}
	m, ok := s.metadata[id]
	if !ok {
		return m, errors.New("missing metadata")
	}
	return m, ctx.Err()
}

func (s *collectionSource) Resolve(ctx context.Context, id string) (source.Metadata, error) {
	return s.Metadata(ctx, id)
}

func pipelineApp(t testing.TB, ch config.Channel, adapter source.SourceAdapter) *App {
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
	return &App{Config: manager, Catalog: db, Source: adapter, ctx: ctx, cancel: cancel, original: manager.Snapshot().Config, representationState: representationState{reps: map[string]media.Representation{}, leases: map[string]time.Time{}}, preparationState: preparationState{prepareWake: make(chan struct{}, 2)}, libraryState: libraryState{scanWake: make(chan struct{}, 1)}}
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
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	var got []string
	for rows.Next() {
		var id string
		var n int
		if err = rows.Scan(&id, &n); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
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
	if err := a.Catalog.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM playlist_members WHERE playlist_id=? AND selected=1 AND video_id IN ('video000001','video000002')`, id).Scan(&old); err != nil || old != 2 {
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

func TestPlaylistFailureIsVisibleAndDoesNotStopOtherCollections(t *testing.T) {
	s := fixtureCollection(4)
	first, second := "PLfixture0001", "PLfixture0002"
	s.lists = []source.Playlist{{ID: first, Title: "First"}, {ID: second, Title: "Second"}}
	s.entries[first] = []source.Entry{{ID: "video000001"}}
	s.entries[second] = []source.Entry{{ID: "video000002"}}
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "latest", Limit: 2}), s)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	s.fail = first
	s.entries[first] = nil
	s.entries[second] = []source.Entry{{ID: "video000003"}}
	if err := a.Sync(t.Context(), testOwner); err == nil {
		t.Fatal("failed collection became a successful job")
	}
	rows, err := a.Catalog.Playlists(t.Context(), testOwner)
	if err != nil || len(rows) != 2 || rows[0].Error == "" || rows[1].Error != "" {
		t.Fatal(rows, err)
	}
	var firstCount, secondCount int
	if err = a.Catalog.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM playlist_members WHERE playlist_id=? AND video_id='video000001' AND selected=1`, first).Scan(&firstCount); err != nil {
		t.Fatal(err)
	}
	if err = a.Catalog.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM playlist_members WHERE playlist_id=? AND video_id='video000003' AND selected=1`, second).Scan(&secondCount); err != nil || firstCount != 1 || secondCount != 1 {
		t.Fatal(firstCount, secondCount, err)
	}
	s.fail = ""
	if err = a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	rows, err = a.Catalog.Playlists(t.Context(), testOwner)
	if err != nil || rows[0].Error != "" {
		t.Fatal(rows, err)
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
	if err := a.prepareRecording(t.Context(), m.ID, false); err != nil {
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
	if err = a.prepareRecording(t.Context(), m.ID, false); err != nil {
		t.Fatal(err)
	}
	v, err = a.Catalog.Video(t.Context(), m.ID)
	if err != nil || v.Preparation != "skipped" {
		t.Fatal(v, err)
	}
}

func TestPolicyMutationAndQueuedPriority(t *testing.T) {
	s := fixtureCollection(2)
	ch := pipelineChannel(config.DefaultSelection())
	a := pipelineApp(t, ch, s)
	a.jobs = make(chan string, 8)
	a.queued = map[string]bool{}
	snap := a.Config.Snapshot()
	if err := a.UpdatePolicy(ch.ID, "", snap.Revision, config.Selection{Mode: "all"}, true); err != nil {
		t.Fatal(err)
	}
	current := a.Config.Snapshot()
	if current.Config.Channels[0].Selection().Mode != "all" {
		t.Fatal("All not saved")
	}
	if err := a.UpdatePolicy(ch.ID, "PLfixture0001", current.Revision, config.Selection{Mode: "skip"}, true); err != nil {
		t.Fatal(err)
	}
	current = a.Config.Snapshot()
	if current.Config.Channels[0].PlaylistSelection("PLfixture0001").Mode != "skip" {
		t.Fatal("Skip not saved")
	}
	if err := a.UpdatePolicy(ch.ID, "PLfixture0001", current.Revision, config.Selection{Mode: "inherit"}, true); err != nil {
		t.Fatal(err)
	}
	current = a.Config.Snapshot()
	if current.Config.Channels[0].PlaylistSelection("PLfixture0001").Mode != "all" {
		t.Fatal("inherit not restored")
	}
	if err := a.UpdatePolicy(ch.ID, "", snap.Revision, config.DefaultSelection(), false); !errors.Is(err, config.ErrConflict) {
		t.Fatal(err)
	}
	if err := a.UpdatePolicy("missing", "", current.Revision, config.DefaultSelection(), false); err == nil {
		t.Fatal("unknown channel policy accepted")
	}
	if err := a.UpdatePolicy(ch.ID, "bad", current.Revision, config.DefaultSelection(), false); err == nil {
		t.Fatal("invalid playlist ID accepted")
	}
}

func TestUnavailableCuratedEntryCanAcquireItsActualUploader(t *testing.T) {
	sourceAdapter := fixtureCollection(0)
	playlist, id := "PLfixture0001", "curated00001"
	sourceAdapter.lists = []source.Playlist{{ID: playlist, ChannelID: testOwner, Title: "Curated"}}
	sourceAdapter.entries[playlist] = []source.Entry{{ID: id}}
	sourceAdapter.unavailable = map[string]bool{id: true}
	a := pipelineApp(t, pipelineChannel(config.DefaultSelection()), sourceAdapter)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	missing, err := a.Catalog.Video(t.Context(), id)
	if err != nil || missing.MetadataAt != 0 || missing.State != "unavailable" {
		t.Fatal(missing, err)
	}
	delete(sourceAdapter.unavailable, id)
	uploader := "UC0000000000000000000099"
	sourceAdapter.metadata[id] = source.Metadata{ID: id, ChannelID: uploader, Channel: "Uploader", Title: "Now public", UploadDate: "20261025", Duration: 60}
	if err = a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	current, err := a.Catalog.Video(t.Context(), id)
	if err != nil || current.ChannelID != uploader || current.Preparation != "queued" {
		t.Fatal(current, err)
	}
}

func TestAllStreamsEachTabOnceAndKeepsPriorSelectionOnFailure(t *testing.T) {
	s := fixtureCollection(80)
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), s)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	for _, scope := range s.calls {
		if scope.Count != 0 || scope.Start != 1 {
			t.Fatalf("All paged the provider: %+v", scope)
		}
	}
	if len(s.calls) != 3 {
		t.Fatalf("source tabs enumerated %d times", len(s.calls))
	}
	s.entries["videos"] = s.entries["videos"][:1]
	s.fail = "streams"
	if err := a.Sync(t.Context(), testOwner); err == nil {
		t.Fatal("incomplete enumeration succeeded")
	}
	page, err := a.Videos(t.Context(), catalog.VideoFilter{Inclusion: "selected"})
	if err != nil || page.Total != 80 {
		t.Fatal(page.Total, err)
	}
	s.fail = ""
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	page, err = a.Videos(t.Context(), catalog.VideoFilter{Inclusion: "selected"})
	if err != nil || page.Total != 1 {
		t.Fatal(page.Total, err)
	}
}

type streamedCollections struct {
	*collectionSource
	rootCalls    int
	failRoot     bool
	largestBatch int
}

func (s *streamedCollections) Playlists(context.Context, string) ([]source.Playlist, error) {
	panic("unbounded playlist list used by streaming application")
}

func (s *streamedCollections) StreamPlaylists(ctx context.Context, _ string, consume func([]source.Playlist) error) error {
	s.rootCalls++
	for start := 0; start < len(s.lists); start += 25 {
		batch := s.lists[start:min(start+25, len(s.lists))]
		s.largestBatch = max(s.largestBatch, len(batch))
		if err := consume(batch); err != nil {
			return err
		}
		if s.failRoot {
			return errors.New("interrupted root playlist enumeration")
		}
	}
	return ctx.Err()
}

func TestStreamedPlaylistRootsPreserveIdentityAcrossRemovalAndFailure(t *testing.T) {
	fixture := fixtureCollection(1)
	for n := range 121 {
		id := fmt.Sprintf("PLfixture%06d", n)
		fixture.lists = append(fixture.lists, source.Playlist{ID: id, ChannelID: testOwner, Title: id})
		fixture.entries[id] = []source.Entry{{ID: "video000000"}}
	}
	adapter := &streamedCollections{collectionSource: fixture}
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "latest", Limit: 1}), adapter)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	if adapter.rootCalls != 1 || adapter.largestBatch != 25 {
		t.Fatal(adapter.rootCalls, adapter.largestBatch)
	}
	countSelected := func() int {
		var count int
		if err := a.Catalog.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM playlist_members WHERE selected=1`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if count := countSelected(); count != 121 {
		t.Fatal(count)
	}
	first := fixture.lists[0]
	adapter.lists = []source.Playlist{{ID: "PLnew00000001", ChannelID: testOwner, Title: "Safe addition"}}
	adapter.failRoot = true
	if err := a.Sync(t.Context(), testOwner); err == nil {
		t.Fatal("incomplete root succeeded")
	}
	if count := countSelected(); count != 121 {
		t.Fatal("partial root removed membership", count)
	}
	adapter.failRoot = false
	adapter.lists = []source.Playlist{first}
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	if count := countSelected(); count != 1 {
		t.Fatal("removed playlists remained exposed", count)
	}
	playlists, err := a.Catalog.Playlists(t.Context(), testOwner)
	if err != nil || len(playlists) != 122 {
		t.Fatal("durable playlist identities removed", len(playlists), err)
	}
	available := 0
	for _, p := range playlists {
		if p.Available {
			available++
			if p.ID != first.ID || p.Season != 1 {
				t.Fatal(p)
			}
		}
	}
	if available != 1 {
		t.Fatal("incorrect source availability", available)
	}
	// A new playlist cannot reuse any historical season assignment.
	adapter.lists = append(adapter.lists, source.Playlist{ID: "PLnew00000002", ChannelID: testOwner, Title: "New collection"})
	adapter.entries["PLnew00000002"] = []source.Entry{{ID: "video000000"}}
	if err = a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	playlists, err = a.Catalog.Playlists(t.Context(), testOwner)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range playlists {
		if p.ID == "PLnew00000002" && p.Season != 123 {
			t.Fatal("season number reused", p)
		}
	}
}

type delayedDiscoverySource struct {
	*collectionSource
	entered chan struct{}
	release chan struct{}
	visits  atomic.Int32
}

func (s *delayedDiscoverySource) Entries(ctx context.Context, scope source.Scope, consume func([]source.Entry) error) (source.Listing, error) {
	if scope.Tab == "videos" && s.visits.Add(1) == 1 {
		close(s.entered)
		select {
		case <-ctx.Done():
			return source.Listing{}, ctx.Err()
		case <-s.release:
		}
	}
	return s.collectionSource.Entries(ctx, scope, consume)
}

func TestChangedPoliciesRunAfterAnInFlightDiscoveryCompletes(t *testing.T) {
	adapter := &delayedDiscoverySource{collectionSource: fixtureCollection(3), entered: make(chan struct{}), release: make(chan struct{})}
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "latest", Limit: 3}), adapter)
	a.queued = map[string]bool{}
	a.attempted = map[string]time.Time{}
	a.jobs = make(chan string, 4)
	t.Cleanup(func() { a.cancel(); a.wg.Wait() })
	a.wg.Add(1)
	go a.worker(a.ctx)
	if err := a.Queue(testOwner); err != nil {
		t.Fatal(err)
	}
	select {
	case <-adapter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not start")
	}
	snapshot := a.Config.Snapshot()
	snapshot.Config.Channels[0].Indexing = &config.Selection{Mode: "latest", Limit: 1}
	if err := a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}
	// Simulate a job which exceeded its interval. Its next automatic attempt must
	// be measured from completion, and changed policy must get one fresh job.
	a.mu.Lock()
	a.attempted[testOwner] = time.Now().Add(-time.Hour)
	a.mu.Unlock()
	close(adapter.release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		jobs, err := a.Catalog.Jobs(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) == 2 && jobs[0].State == "complete" {
			page, err := a.Videos(t.Context(), catalog.VideoFilter{Inclusion: "selected"})
			if err != nil || page.Total != 1 {
				t.Fatal(page.Total, err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("changed policy was not reconciled", jobs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.mu.RLock()
	attempted := a.attempted[testOwner]
	a.mu.RUnlock()
	if time.Since(attempted) > time.Minute {
		t.Fatal("refresh interval still starts at the old job's start")
	}
	if adapter.visits.Load() != 2 {
		t.Fatal("changed policy triggered repeated jobs", adapter.visits.Load())
	}
}
