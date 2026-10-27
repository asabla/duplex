package app

import (
	"errors"
	"io/fs"
	"path"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/config"
)

func TestStartupPublicationScansRetainedLibraryWithoutVisibleChannels(t *testing.T) {
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	projectionVideo(t, a, "video000001", 1)

	snapshot := a.Config.Snapshot()

	snapshot.Config.Channels = nil
	if err := a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}

	if err := a.Publish(); err != nil {
		t.Fatal(err)
	}

	if !a.scanPending {
		t.Fatal("startup with no visible channels did not scan retained Plex entries")
	}
}

func TestCollectionVisibilityReturnAdvancesDirectoryTimesWithoutDiscovery(t *testing.T) {
	for _, scenario := range []string{"pause", "unfollow", "playlists", "playlist"} {
		t.Run(scenario, func(t *testing.T) {
			a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
			prepared := projectionVideo(t, a, "video000001", 1)

			playlist := "PLfixture0001"
			if err := a.Catalog.SavePlaylist(t.Context(), catalog.Playlist{ID: playlist, ChannelID: testOwner, Title: "Controlled playlist"}); err != nil {
				t.Fatal(err)
			}

			if err := a.Catalog.AddPlaylistMember(t.Context(), playlist, prepared.Metadata.ID, 1, true); err != nil {
				t.Fatal(err)
			}

			if err := a.Publish(); err != nil {
				t.Fatal(err)
			}

			a.scanPending = false
			provider := catalogueFS{app: a}
			directory := path.Join("Channel-"+testOwner, "Season 0001")

			before, err := provider.Stat(t.Context(), directory)
			if err != nil {
				t.Fatal(err)
			}

			snapshot := a.Config.Snapshot()
			original := snapshot.Config.Channels[0]
			channel := original
			channel.Playlists = &config.PlaylistPolicy{Enabled: true}

			switch scenario {
			case "pause":
				channel.Enabled = false
			case "unfollow":
				snapshot.Config.Channels = nil
			case "playlists":
				channel.Playlists.Enabled = false
			case "playlist":
				channel.Playlists.Overrides = map[string]config.Selection{playlist: {Mode: "skip"}}
			}

			if scenario != "unfollow" {
				snapshot.Config.Channels = []config.Channel{channel}
			}

			if err = a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
				t.Fatal(err)
			}

			if err = a.Publish(); err != nil {
				t.Fatal(err)
			}

			if _, err = provider.Stat(t.Context(), directory); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("hidden collection remained visible", err)
			}

			if !a.scanPending {
				t.Error("hiding a collection did not request a scan")
			}

			a.scanPending = false
			snapshot = a.Config.Snapshot()

			snapshot.Config.Channels = []config.Channel{original}
			if err = a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
				t.Fatal(err)
			}

			if err = a.Publish(); err != nil {
				t.Fatal(err)
			}

			after, err := provider.Stat(t.Context(), directory)
			if err != nil || after.Modified <= before.Modified || !a.scanPending {
				t.Fatal("returned collection retained its timestamp or missed its scan", before, after, a.scanPending, err)
			}

			video, err := a.Catalog.Video(t.Context(), prepared.Metadata.ID)
			if err != nil || video.Revision != prepared.Revision || video.Number != 1 {
				t.Fatal("visibility changed recording identity", video, err)
			}

			a.scanPending = false
			if err = a.Publish(); err != nil {
				t.Fatal(err)
			}

			unchanged, err := provider.Stat(t.Context(), directory)
			if err != nil || unchanged.Modified != after.Modified || a.scanPending {
				t.Fatal("unchanged publication changed the collection", unchanged, a.scanPending, err)
			}

			// Unrelated settings must not alter directory times or request scans.
			snapshot = a.Config.Snapshot()

			snapshot.Config.RefreshMinutes++
			if err = a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
				t.Fatal(err)
			}

			if err = a.Publish(); err != nil {
				t.Fatal(err)
			}

			unchanged, err = provider.Stat(t.Context(), directory)
			if err != nil || unchanged.Modified != after.Modified || a.scanPending {
				t.Fatal("unrelated settings changed the collection", unchanged, a.scanPending, err)
			}
		})
	}
}
