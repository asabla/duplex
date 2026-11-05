package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestChannelLookupCopiesOnlyRequestedPolicyAndTracksChanges(t *testing.T) {
	selection := DefaultSelection()
	ch := Channel{ID: "target", Enabled: true, Unlisted: []string{"video"}, Indexing: &selection, Content: &ContentPolicy{}, VideoOverrides: map[string]string{"video": "keep"}, Playlists: &PlaylistPolicy{Enabled: true, Overrides: map[string]Selection{"playlist": selection}}, Artwork: Artwork{Seasons: map[string]string{"year:2026": "poster"}}}
	m := &Manager{current: Snapshot{Config: Config{Channels: []Channel{ch}}}}
	want := m.Snapshot().Config.Channels[0]

	got, ok := m.Channel("target")
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatal("lookup differs from isolated snapshot")
	}

	got.Unlisted[0] = "changed"
	got.Indexing.Limit = 1
	got.Content.Uploads.Mode = "skip"
	got.VideoOverrides["video"] = "exclude"
	got.Playlists.Overrides["playlist"] = Selection{Mode: "skip"}
	got.Artwork.Seasons["year:2026"] = "changed"

	if current, _ := m.Channel("target"); !reflect.DeepEqual(current, want) {
		t.Fatal("lookup leaked mutable policy")
	}

	if _, ok = m.Channel("missing"); ok {
		t.Fatal("missing channel returned")
	}

	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := Create(file, filepath.Join(t.TempDir(), "state")); err != nil {
		t.Fatal(err)
	}

	manager, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}

	state := manager.Snapshot()

	state.Config.Channels = []Channel{{ID: "UCaaaaaaaaaaaaaaaaaaaaaa", URL: "https://www.youtube.com/channel/UCaaaaaaaaaaaaaaaaaaaaaa", Enabled: true, Limit: 50}}
	if err = manager.Save(state.Config, state.Revision); err != nil {
		t.Fatal(err)
	}

	if current, ok := manager.Channel(state.Config.Channels[0].ID); !ok || !current.Enabled {
		t.Fatal("saved channel not indexed")
	}

	if current, ok := manager.LibraryChannel(state.Config.Channels[0].ID); !ok || !current.Enabled {
		t.Fatal("saved library policy not indexed")
	}

	state = manager.Snapshot()
	state.Config.Channels[0].Enabled = false

	data, err := yaml.Marshal(state.Config)
	if err != nil {
		t.Fatal(err)
	}

	if err = os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if !manager.Reload() {
		t.Fatal("valid reload rejected")
	}

	if current, _ := manager.Channel(state.Config.Channels[0].ID); current.Enabled {
		t.Fatal("reload retained stale lookup")
	}

	if current, _ := manager.LibraryChannel(state.Config.Channels[0].ID); current.Enabled {
		t.Fatal("reload retained stale library policy")
	}
}

func TestLibraryPolicyLookupExcludesLargeSelectionMaps(t *testing.T) {
	allocations := func(overrides int) float64 {
		ch := Channel{ID: "target", Enabled: true, VideoOverrides: map[string]string{}, Playlists: &PlaylistPolicy{Enabled: true, Overrides: map[string]Selection{"skip": {Mode: "skip"}, "keep": {Mode: "all"}}}}
		for i := range overrides {
			ch.VideoOverrides[fmt.Sprint(i)] = "keep"
		}

		m := &Manager{current: Snapshot{Config: Config{Channels: []Channel{ch, {ID: "disabled"}}}}}

		policy, ok := m.LibraryChannel("target")
		if !ok || !policy.Playlists || !reflect.DeepEqual(policy.SkippedPlaylists, []string{"skip"}) {
			t.Fatal("visibility policy changed", policy)
		}

		policy.SkippedPlaylists[0] = "changed"
		if current, _ := m.LibraryChannel("target"); current.SkippedPlaylists[0] != "skip" {
			t.Fatal("library lookup leaked skip policy")
		}

		all := m.LibraryChannels()
		if len(all) != 2 || all[0].ID != "target" || all[1].Enabled {
			t.Fatal("library root order or enabled state changed", all)
		}

		all[0].SkippedPlaylists[0] = "changed"
		if current, _ := m.LibraryChannel("target"); current.SkippedPlaylists[0] != "skip" {
			t.Fatal("library list leaked skip policy")
		}

		if _, ok = m.LibraryChannel("missing"); ok {
			t.Fatal("missing channel policy returned")
		}

		if m.LoadedAt() != m.Snapshot().LoadedAt {
			t.Fatal("timestamp differs from applied snapshot")
		}

		return testing.AllocsPerRun(100, func() { m.LibraryChannel("target") })
	}
	if small, large := allocations(1), allocations(10000); large > small || large > 1 {
		t.Fatal("filesystem policy copied media selection rules", small, large)
	}
}

func TestChannelLookupAllocationDoesNotGrowWithOtherChannels(t *testing.T) {
	allocations := func(count int) float64 {
		m := &Manager{}
		for i := range count {
			m.current.Config.Channels = append(m.current.Config.Channels, Channel{ID: fmt.Sprint(i), VideoOverrides: map[string]string{"video": "keep"}})
		}

		m.Channel("0")

		return testing.AllocsPerRun(100, func() { m.Channel("0") })
	}
	if small, large := allocations(1), allocations(1000); large > small+1 {
		t.Fatal("lookup copied unrelated channels", small, large)
	}
}
