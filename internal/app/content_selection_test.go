package app

import (
	"errors"
	"reflect"
	"sort"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/source"
)

func mixedContent(t *testing.T) (*App, *collectionSource) {
	t.Helper()

	s := fixtureCollection(7)
	s.entries["videos"] = []source.Entry{{ID: "video000000"}, {ID: "video000001"}, {ID: "video000004"}}
	s.entries["streams"] = []source.Entry{{ID: "video000002"}, {ID: "video000003"}}

	s.entries["shorts"] = []source.Entry{{ID: "video000005"}, {ID: "video000006"}}
	for _, id := range []string{"video000002", "video000003"} {
		m := s.metadata[id]
		m.LiveStatus = "was_live"
		s.metadata[id] = m
	}

	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), s)
	a.jobs = make(chan string, 8)
	a.queued = map[string]bool{}

	return a, s
}

func selectedIDs(t *testing.T, a *App) []string {
	t.Helper()

	p, err := a.Videos(t.Context(), catalog.VideoFilter{Inclusion: "selected", PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}

	ids := []string{}
	for _, v := range p.Items {
		ids = append(ids, v.ID)
	}

	sort.Strings(ids)

	return ids
}

func TestSeparateContentWindowsKeepAndExclude(t *testing.T) {
	a, adapter := mixedContent(t)
	snapshot := a.Config.Snapshot()
	ch := snapshot.Config.Channels[0]
	ch.Content = &config.ContentPolicy{Uploads: config.Selection{Mode: "latest", Limit: 1}, Shorts: config.Selection{Mode: "skip"}, Replays: config.Selection{Mode: "latest", Limit: 1}}
	ch.VideoOverrides = map[string]string{"video000000": "exclude", "video000004": "keep"}

	snapshot.Config.Channels[0] = ch
	if err := a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}

	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	want := []string{"video000001", "video000002", "video000004"}
	if got := selectedIDs(t, a); !reflect.DeepEqual(got, want) {
		t.Fatal("independent windows, exclusion refill, or additive keep failed", got)
	}

	for _, call := range adapter.calls {
		if call.Tab == "shorts" {
			t.Fatal("skipped Shorts were enumerated", call)
		}
	}

	preview, err := a.Catalog.PreviewUploads(t.Context(), ch)
	if err != nil || preview.Selected != 3 || preview.Added != 0 || preview.Removed != 0 {
		t.Fatal(preview, err)
	}

	for _, id := range want {
		m := adapter.metadata[id]
		if err = a.Catalog.SavePrepared(t.Context(), catalog.Prepared{Metadata: m, Revision: "fixed-" + id, Height: 1080, Size: 100}); err != nil {
			t.Fatal(err)
		}
	}

	before, err := a.Catalog.Video(t.Context(), "video000004")
	if err != nil {
		t.Fatal(err)
	}

	if err = a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	after, err := a.Catalog.Video(t.Context(), "video000004")
	if err != nil || before.Revision != after.Revision || before.Number != after.Number || before.Updated != after.Updated {
		t.Fatal("unchanged refresh changed media identity or time", before, after, err)
	}
}

func TestPreviewAndKnownRulesDoNotFetchSources(t *testing.T) {
	a, adapter := mixedContent(t)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	snapshot := a.Config.Snapshot()
	change := SelectionChange{ChannelID: testOwner, Revision: snapshot.Revision, Selection: config.Selection{Mode: "latest", Limit: 1}, Content: &config.ContentPolicy{Uploads: config.Selection{Mode: "latest", Limit: 1}, Shorts: config.Selection{Mode: "skip"}, Replays: config.Selection{Mode: "skip"}}, VideoOverrides: map[string]string{"video000000": "exclude", "video000004": "keep"}}
	calls, resolved := len(adapter.calls), adapter.resolved

	preview, err := a.PreviewSelection(t.Context(), change)
	if err != nil || preview.Selected != 2 || preview.Removed != 5 {
		t.Fatal(preview, err)
	}

	if len(adapter.calls) != calls || adapter.resolved != resolved || a.Config.Snapshot().Revision != snapshot.Revision || len(selectedIDs(t, a)) != 7 {
		t.Fatal("preview fetched, saved, or changed membership")
	}

	if err = a.SaveSelection(t.Context(), change); err != nil {
		t.Fatal(err)
	}

	if got := selectedIDs(t, a); !reflect.DeepEqual(got, []string{"video000001", "video000004"}) {
		t.Fatal("known rules were not applied before discovery", got)
	}

	if len(adapter.calls) != calls || adapter.resolved != resolved {
		t.Fatal("saving known rules fetched sources synchronously")
	}

	if _, err = a.PreviewSelection(t.Context(), change); !errors.Is(err, config.ErrConflict) {
		t.Fatal("stale preview was accepted", err)
	}
}

func TestFailedContentRefreshRetainsPreviousWindow(t *testing.T) {
	a, adapter := mixedContent(t)
	snapshot := a.Config.Snapshot()

	snapshot.Config.Channels[0].Content = &config.ContentPolicy{Uploads: config.Selection{Mode: "all"}, Shorts: config.Selection{Mode: "skip"}, Replays: config.Selection{Mode: "all"}}
	if err := a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}

	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	before := selectedIDs(t, a)
	adapter.entries["videos"] = nil
	adapter.fail = "streams"

	if err := a.Sync(t.Context(), testOwner); err == nil {
		t.Fatal("failed stream enumeration was accepted")
	}

	if got := selectedIDs(t, a); !reflect.DeepEqual(got, before) {
		t.Fatal("partial enumeration removed the previous window", got, before)
	}
}

func TestNewRulesDoNotRestoreWithdrawnEntriesAfterSuccessfulDiscovery(t *testing.T) {
	a, adapter := mixedContent(t)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	snapshot := a.Config.Snapshot()

	snapshot.Config.Channels[0].Content = &config.ContentPolicy{Uploads: config.Selection{Mode: "all"}, Shorts: config.Selection{Mode: "skip"}, Replays: config.Selection{Mode: "skip"}}
	if err := a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}

	adapter.entries["videos"] = []source.Entry{{ID: "video000001"}}

	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	if got := selectedIDs(t, a); !reflect.DeepEqual(got, []string{"video000001"}) {
		t.Fatal("publication restored a withdrawn source entry", got)
	}

	// A new process has no runtime publication bookkeeping.
	a.publicationRevision = ""

	a.publicationSelections = nil
	if err := a.Publish(); err != nil {
		t.Fatal(err)
	}

	if got := selectedIDs(t, a); !reflect.DeepEqual(got, []string{"video000001"}) {
		t.Fatal("restart restored a withdrawn source entry", got)
	}
}

func TestRestoringSharedSelectionDoesNotRestoreWithdrawnEntries(t *testing.T) {
	a, adapter := mixedContent(t)
	snapshot := a.Config.Snapshot()

	snapshot.Config.Channels[0].Content = &config.ContentPolicy{Uploads: config.Selection{Mode: "all"}, Shorts: config.Selection{Mode: "skip"}, Replays: config.Selection{Mode: "skip"}}
	if err := a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}

	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	snapshot = a.Config.Snapshot()

	snapshot.Config.Channels[0].Content = nil
	if err := a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}

	adapter.entries = map[string][]source.Entry{"videos": {{ID: "video000001"}}}

	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	if got := selectedIDs(t, a); !reflect.DeepEqual(got, []string{"video000001"}) {
		t.Fatal("restoring shared rules restored withdrawn recordings", got)
	}

	snapshot = a.Config.Snapshot()

	snapshot.Config.Channels[0].Indexing = &config.Selection{Mode: "latest", Limit: 1}
	if err := a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}

	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	a.publicationRevision = ""

	a.publicationSelections = nil
	if err := a.Publish(); err != nil {
		t.Fatal(err)
	}

	if got := selectedIDs(t, a); !reflect.DeepEqual(got, []string{"video000001"}) {
		t.Fatal("restart restored withdrawn recordings under shared rules", got)
	}
}
