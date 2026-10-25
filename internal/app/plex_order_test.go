package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/plex"
)

type orderFixtureItem struct {
	key, guid string
	index     int
}

type orderFixture struct {
	mu        sync.Mutex
	items     []orderFixtureItem
	pages     int
	mutations int
	failAt    int
	failed    bool
}

func (f *orderFixture) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodGet {
			f.pages++
			start, err := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
			if err != nil {
				t.Error(err)
			}
			sorted := append([]orderFixtureItem(nil), f.items...)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i].index < sorted[j].index })
			end := min(start+100, len(sorted))
			body := fmt.Sprintf(`<MediaContainer offset="%d" totalSize="%d">`, start, len(sorted))
			for _, item := range sorted[start:end] {
				body += fmt.Sprintf(`<Video ratingKey="%s" guid="%s" index="%d"/>`, item.key, item.guid, item.index)
			}
			if _, err = w.Write([]byte(body + "</MediaContainer>")); err != nil {
				t.Error(err)
			}
			return
		}
		if r.Method != http.MethodPut || r.URL.Path != "/library/sections/7/all" || f.pages < (len(f.items)+99)/100 {
			t.Error("Plex was mutated before its complete snapshot was read")
			http.Error(w, "unexpected mutation", http.StatusBadRequest)
			return
		}
		if f.mutations == f.failAt && !f.failed {
			f.failed = true
			http.Error(w, "controlled upstream failure", http.StatusServiceUnavailable)
			return
		}
		key := r.URL.Query().Get("id")
		index, err := strconv.Atoi(r.URL.Query().Get("index.value"))
		if err != nil || r.URL.Query().Has("index.locked") {
			t.Error("invalid or locked episode index")
			http.Error(w, "invalid index", http.StatusBadRequest)
			return
		}
		for _, item := range f.items {
			if item.key != key && item.index == index {
				t.Error("temporary duplicate episode index", index)
				http.Error(w, "index collision", http.StatusConflict)
				return
			}
		}
		for i := range f.items {
			if f.items[i].key == key {
				f.items[i].index = index
				f.mutations++
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.NotFound(w, r)
	})
}

func TestPlexReorderSnapshotsAllPagesAndResumesAfterPartialMutation(t *testing.T) {
	const count = 205
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	if err := a.Catalog.EnsureChannel(t.Context(), testOwner, "Channel"); err != nil {
		t.Fatal(err)
	}
	if err := a.Catalog.SavePlaylist(t.Context(), catalog.Playlist{ID: "PLfixture0001", ChannelID: testOwner, Title: "Ordered"}); err != nil {
		t.Fatal(err)
	}
	fixture := &orderFixture{failAt: 5}
	for i := range count {
		id := fmt.Sprintf("video%06d", i)
		projectionVideo(t, a, id, i+1)
		if err := a.Catalog.AddPlaylistMember(t.Context(), "PLfixture0001", id, i+1, true); err != nil {
			t.Fatal(err)
		}
		occurrence := catalog.PlaylistOccurrenceID(testOwner, "PLfixture0001", id)
		fixture.items = append(fixture.items, orderFixtureItem{key: strconv.Itoa(i + 1000), index: i + 1, guid: "tv.plex.agents.nfo.series://episode/" + url.PathEscape(occurrence)})
	}
	run, err := a.Catalog.StartJob(t.Context(), testOwner)
	if err != nil {
		t.Fatal(err)
	}
	// Remove the first occurrence, and reverse all remaining occurrences. Its
	// previous index must be vacated before a different video can use index one.
	for i := count - 1; i >= 1; i-- {
		if err = a.Catalog.StageDiscovery(t.Context(), run, "PLfixture0001", fmt.Sprintf("video%06d", i), count-i); err != nil {
			t.Fatal(err)
		}
	}
	if err = a.Catalog.ReconcileDiscovery(t.Context(), run, "PLfixture0001", testOwner, 0); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fixture.handler(t))
	defer server.Close()
	client, err := plex.New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	connection := config.Plex{URL: server.URL, Section: "7"}
	if err = a.bindPlex(t.Context(), client, connection); err == nil {
		t.Fatal("controlled partial mutation failure became success")
	}
	if err = a.bindPlex(t.Context(), client, connection); err != nil {
		t.Fatal("retry did not reconcile actual observed state", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.pages != 6 || !fixture.failed {
		t.Fatal("missing complete snapshot on retry", fixture.pages, fixture.failed)
	}
	for i, item := range fixture.items {
		want := count - i
		if i == 0 {
			want = 1<<30 + 1
		}
		if item.index != want {
			t.Fatal("wrong final episode order", item.key, item.index, want)
		}
	}
}
