package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/library"
	"duplex/internal/media"
	"duplex/internal/source"
)

type mountedBytes struct {
	id   string
	data []byte
}

func TestFilesystemVersionReadsNeverReturnReplacementBytes(t *testing.T) {
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	prepared := projectionVideo(t, a, "video000001", 1)
	p := catalogueFS{app: a}
	name := fmt.Sprintf("Channel-%s/Season 2026/Channel-%s - S2026E0001.mp4", testOwner, testOwner)
	original := mountedBytes{id: prepared.Revision, data: []byte("immutable bytes!")}
	a.cacheRepresentation(original, time.Now().Add(time.Hour), prepared.Metadata.ID)

	if _, err := a.Catalog.DB.ExecContext(t.Context(), `UPDATE videos SET revision='replacement' WHERE id=?`, prepared.Metadata.ID); err != nil {
		t.Fatal(err)
	}

	rep, err := p.OpenVersion(t.Context(), name, prepared.Revision)
	if err != nil || rep.ID() != prepared.Revision {
		t.Fatal("cached old revision was replaced", err)
	}

	a.mu.Lock()
	delete(a.reps, prepared.Revision)
	a.mu.Unlock()

	if _, err = p.OpenVersion(t.Context(), name, prepared.Revision); !errors.Is(err, media.ErrChanged) {
		t.Fatal("evicted old revision did not fail", err)
	}

	if _, err = p.OpenVersion(t.Context(), "missing.nfo", "revision"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}

	nfo := strings.TrimSuffix(name, ".mp4") + ".nfo"

	info, err := p.Stat(t.Context(), nfo)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = p.OpenVersion(t.Context(), nfo, info.Revision); err != nil {
		t.Fatal(err)
	}

	prepared.Metadata.Title = "Replacement metadata"
	if err = a.Catalog.SaveMetadata(t.Context(), prepared.Metadata); err != nil {
		t.Fatal(err)
	}

	if _, err = p.OpenVersion(t.Context(), nfo, info.Revision); !errors.Is(err, media.ErrChanged) {
		t.Fatal("old metadata handle served changed bytes", err)
	}
}

func (r mountedBytes) ID() string  { return r.id }
func (r mountedBytes) Size() int64 { return int64(len(r.data)) }
func (r mountedBytes) ReadAt(ctx context.Context, p []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	return bytes.NewReader(r.data).ReadAt(p, offset)
}

func projectionVideo(t *testing.T, a *App, id string, number int) catalog.Prepared {
	t.Helper()

	m := source.Metadata{ID: id, ChannelID: testOwner, Channel: "Example channel", Title: "Video " + id, UploadDate: "20261025", Duration: 60}
	if err := a.Catalog.SaveMetadata(t.Context(), m); err != nil {
		t.Fatal(err)
	}

	p := catalog.Prepared{Metadata: m, Revision: "revision-" + id, Height: 1080, RequestedHeight: 2160, Size: 16}
	if err := a.Catalog.SavePrepared(t.Context(), p); err != nil {
		t.Fatal(err)
	}

	if err := a.Catalog.MarkUploadedSelection(t.Context(), id); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Catalog.DB.ExecContext(t.Context(), `UPDATE videos SET number=? WHERE id=?`, number, id); err != nil {
		t.Fatal(err)
	}

	return p
}

func TestCatalogueFilesystemUsesDurableSizesAndStableHandles(t *testing.T) {
	adapter := fixtureCollection(0)
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), adapter)
	prepared := projectionVideo(t, a, "video000001", 7)
	provider := catalogueFS{app: a}
	base := fmt.Sprintf("Channel-%s/Season 2026/Channel-%s - S2026E0007", testOwner, testOwner)

	info, err := provider.Stat(t.Context(), base+".mp4")
	if err != nil || info.Size != 16 || info.Directory || adapter.resolved != 0 {
		t.Fatal(info, err, adapter.resolved)
	}

	rep := mountedBytes{id: prepared.Revision, data: []byte("immutable bytes!")}
	a.cacheRepresentation(rep, time.Now().Add(time.Hour), prepared.Metadata.ID)

	handle, _, err := provider.Open(t.Context(), base+".mp4")
	if err != nil || handle.ID() != prepared.Revision {
		t.Fatal(handle, err)
	}

	// Eviction removes only the cache reference. An open handle keeps its bytes.
	for n := range 140 {
		a.cacheRepresentation(mountedBytes{id: fmt.Sprintf("other-%d", n), data: []byte("other")}, time.Now().Add(time.Hour), fmt.Sprintf("video%06d", n))
	}

	if len(a.reps) > runtimeRepresentations || a.reps[prepared.Revision] != nil {
		t.Fatal("runtime cache is unbounded or did not evict")
	}

	output := make([]byte, 16)
	if _, err = handle.ReadAt(t.Context(), output, 0); err != nil || string(output) != "immutable bytes!" {
		t.Fatal(string(output), err)
	}

	if adapter.resolved != 0 {
		t.Fatal("stat or cached open resolved sources")
	}

	nfo, _, err := provider.Open(t.Context(), base+".nfo")
	if err != nil {
		t.Fatal(err)
	}

	data := make([]byte, nfo.Size())
	if _, err = nfo.ReadAt(t.Context(), data, 0); err != nil || !strings.Contains(string(data), "youtube-video:video000001") {
		t.Fatal(string(data), err)
	}

	show, _, err := provider.Open(t.Context(), "Channel-"+testOwner+"/tvshow.nfo")
	if err != nil || show.Size() == 0 {
		t.Fatal(show, err)
	}

	for _, name := range []string{"../secret", "/absolute", "Channel-missing", "Channel-missing/Season 2026", "Channel-" + testOwner + "/Season 1", "Channel-" + testOwner + "/Season 2025", "Channel-" + testOwner + "/Season 2026/invalid.mp4", base + ".unknown", base + ".mp4/child"} {
		if _, err = provider.Stat(t.Context(), name); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(name, err)
		}

		if _, err = provider.ReadDir(t.Context(), name, "", 1); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("listed invalid directory", name, err)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err = provider.ReadDir(ctx, "Channel-"+testOwner+"/Season 2026", "", 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if _, _, err = provider.Open(t.Context(), "Channel-"+testOwner); err == nil {
		t.Fatal("opened a directory as media")
	}

	if _, err = provider.ReadDir(t.Context(), base+".nfo", "", 1); err == nil {
		t.Fatal("listed a file as a directory")
	}
}

func TestCatalogueFilesystemLexicalPagingAcrossLargeEpisodeNumbers(t *testing.T) {
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	for i, number := range []int{1000, 9999, 10000, 10001} {
		projectionVideo(t, a, fmt.Sprintf("video%06d", i), number)
	}

	assets := []library.Entry{}

	for _, suffix := range []string{".jpg", ".en.srt"} {
		entry, err := a.saveAsset("video000002", suffix, []byte("local sidecar fixture"))
		if err != nil {
			t.Fatal(err)
		}

		assets = append(assets, entry)
	}

	if err := a.saveAssetManifest("video000002", assets); err != nil {
		t.Fatal(err)
	}

	provider := catalogueFS{app: a}
	directory := "Channel-" + testOwner + "/Season 2026"

	var expected []string

	for _, number := range []int{1000, 9999, 10000, 10001} {
		suffixes := []string{".mp4", ".nfo"}
		if number == 10000 {
			suffixes = append(suffixes, ".jpg", ".en.srt")
		}

		for _, suffix := range suffixes {
			expected = append(expected, fmt.Sprintf("Channel-%s - S2026E%04d%s", testOwner, number, suffix))
		}
	}

	sort.Strings(expected)

	for _, size := range []int{1, 2, 3, 25, 128, 256} {
		after := ""
		got := []string{}

		for range 20 {
			page, err := provider.ReadDir(t.Context(), directory, after, size)
			if err != nil || len(page) > size {
				t.Fatal(page, err)
			}

			if len(page) == 0 {
				break
			}

			for _, child := range page {
				if child.Directory || child.Name <= after {
					t.Fatal(child, after)
				}

				got = append(got, child.Name)
				after = child.Name
			}
		}

		if !reflect.DeepEqual(got, expected) {
			t.Fatal(size, got, expected)
		}
	}

	root, err := provider.ReadDir(t.Context(), "", "", 1)
	if err != nil || len(root) != 1 || root[0].Name != "Channel-"+testOwner {
		t.Fatal(root, err)
	}

	channel, err := provider.ReadDir(t.Context(), "Channel-"+testOwner, "", 1)
	if err != nil || len(channel) != 1 || channel[0].Name != "Season 2026" {
		t.Fatal(channel, err)
	}

	channel, err = provider.ReadDir(t.Context(), "Channel-"+testOwner, "Season 2026", 1)
	if err != nil || len(channel) != 1 || channel[0].Name != "tvshow.nfo" {
		t.Fatal(channel, err)
	}

	for _, size := range []int{0, 257} {
		if _, err = provider.ReadDir(t.Context(), directory, "", size); err == nil {
			t.Fatal("unbounded page accepted", size)
		}
	}
}

func TestCatalogueFilesystemPlaylistPoliciesAndIndependentOccurrenceIDs(t *testing.T) {
	ch := pipelineChannel(config.Selection{Mode: "all"})
	a := pipelineApp(t, ch, fixtureCollection(0))
	projectionVideo(t, a, "video000001", 1)

	for _, id := range []string{"PLfixture0001", "PLfixture0002"} {
		if err := a.Catalog.SavePlaylist(t.Context(), catalog.Playlist{ID: id, ChannelID: testOwner, Title: "Playlist " + id}); err != nil {
			t.Fatal(err)
		}

		if err := a.Catalog.AddPlaylistMember(t.Context(), id, "video000001", 1, true); err != nil {
			t.Fatal(err)
		}
	}

	provider := catalogueFS{app: a}

	for season := 1; season <= 2; season++ {
		base := fmt.Sprintf("Channel-%s/Season %04d/Channel-%s - S%04dE0001.nfo", testOwner, season, testOwner, season)

		rep, _, err := provider.Open(t.Context(), base)
		if err != nil {
			t.Fatal(err)
		}

		data := make([]byte, rep.Size())
		if _, err = rep.ReadAt(t.Context(), data, 0); err != nil || !strings.Contains(string(data), fmt.Sprintf("youtube-playlist:%s:PLfixture000%d:video000001", testOwner, season)) {
			t.Fatal(string(data), err)
		}
	}

	snapshot := a.Config.Snapshot()

	snapshot.Config.Channels[0].Playlists.Overrides = map[string]config.Selection{"PLfixture0001": {Mode: "skip"}}
	if err := a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.Stat(t.Context(), path.Join("Channel-"+testOwner, "Season 0001")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("skipped playlist visible", err)
	}

	if _, err := provider.Stat(t.Context(), path.Join("Channel-"+testOwner, "Season 0002")); err != nil {
		t.Fatal(err)
	}

	for _, after := range []string{"", "Channel-" + testOwner + " - S0001E9999.nfo"} {
		if _, err := provider.ReadDir(t.Context(), path.Join("Channel-"+testOwner, "Season 0001"), after, 1); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("skipped playlist directory visible", after, err)
		}
	}

	show, _, err := provider.Open(t.Context(), "Channel-"+testOwner+"/tvshow.nfo")
	if err != nil {
		t.Fatal(err)
	}

	data := make([]byte, show.Size())
	if _, err = show.ReadAt(t.Context(), data, 0); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(data), `number="1"`) || !strings.Contains(string(data), `number="2"`) {
		t.Fatal(string(data))
	}
}

func TestFailedBindingIsVisibleAndDoesNotDiscardTheVerifiedRevision(t *testing.T) {
	adapter := fixtureCollection(0)
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), adapter)
	prepared := projectionVideo(t, a, "video000001", 1)
	provider := catalogueFS{app: a}

	name := fmt.Sprintf("Channel-%s/Season 2026/Channel-%s - S2026E0001.mp4", testOwner, testOwner)
	if _, _, err := provider.Open(t.Context(), name); err == nil {
		t.Fatal("missing source became a successful handle")
	}

	stored, err := a.Catalog.Video(t.Context(), prepared.Metadata.ID)
	if err != nil || stored.Revision != prepared.Revision || stored.Preparation != "failed" || stored.PreparationError == "" || strings.Contains(stored.PreparationError, "higher-quality") {
		t.Fatal(stored, err)
	}

	// A manual refresh can retry a failed bind even when a prior revision exists.
	if err = a.queuePreparation(t.Context(), prepared.Metadata.ID, 0); err != nil {
		t.Fatal(err)
	}

	queued, err := a.Catalog.PendingPreparations(t.Context())
	if err != nil || queued != 1 {
		t.Fatal(queued, err)
	}

	adapter.unavailable = map[string]bool{prepared.Metadata.ID: true}

	if _, _, err = provider.Open(t.Context(), name); !errors.Is(err, source.ErrUnavailable) {
		t.Fatal(err)
	}

	stored, err = a.Catalog.Video(t.Context(), prepared.Metadata.ID)
	if err != nil || stored.Revision != prepared.Revision || stored.State != "unavailable" || stored.ChannelID != testOwner {
		t.Fatal(stored, err)
	}

	if _, err = provider.Stat(t.Context(), name); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("unavailable recording remained exposed", err)
	}
}
