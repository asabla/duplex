package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectionInheritanceAndLegacyDefaults(t *testing.T) {
	c := Channel{Limit: 30}
	if c.Selection().Limit != 30 || c.PlaylistSelection("PLabcdefghij").Mode != "skip" {
		t.Fatal("legacy settings changed")
	}

	p := DefaultSelection()
	c.Indexing = &p

	c.Playlists = &PlaylistPolicy{Enabled: true, Overrides: map[string]Selection{}}
	if c.PlaylistSelection("PLabcdefghij") != p {
		t.Fatal("playlist did not inherit channel policy")
	}

	for _, mode := range []string{"all", "skip", "inherit", "latest"} {
		selection := Selection{Mode: mode}
		if mode == "latest" {
			selection.Limit = 7
		}

		c.Playlists.Overrides["PLabcdefghij"] = selection

		want := selection
		if mode == "inherit" {
			want = p
		}

		if c.PlaylistSelection("PLabcdefghij") != want {
			t.Fatal(mode)
		}
	}

	c.Playlists.Enabled = false
	if c.PlaylistSelection("PLabcdefghij").Mode != "skip" {
		t.Fatal("channel skip must take precedence")
	}
}

func TestPolicyAndArtworkValidation(t *testing.T) {
	for _, selection := range []Selection{{Mode: "latest"}, {Mode: "all", Limit: 3}, {Mode: "skip"}, {Mode: "inherit"}, {Mode: "unknown"}, {Mode: "latest", Limit: -1}} {
		if selection.validate(false) == nil {
			t.Fatalf("invalid channel policy accepted: %+v", selection)
		}
	}

	for _, selection := range []Selection{{Mode: "all"}, {Mode: "latest", Limit: 100000}, {Mode: "latest", Limit: 1}} {
		if err := selection.validate(false); err != nil {
			t.Fatal(err)
		}
	}

	if !ValidArtworkReference(strings.Repeat("a", 64)+".jpg") || ValidArtworkReference("../poster.jpg") {
		t.Fatal("artwork references must be content-addressed")
	}

	if validSeasonScope("year:0") || validSeasonScope("playlist:../../") || !validSeasonScope("year:2026") || !validSeasonScope("playlist:PLabcdefghij") {
		t.Fatal("season scope")
	}

	c := Channel{Limit: 50, Artwork: Artwork{Poster: "bad"}}
	if c.validatePolicy() == nil {
		t.Fatal("invalid poster accepted")
	}

	c.Artwork = Artwork{Seasons: map[string]string{"year:2026": "bad"}}
	if c.validatePolicy() == nil {
		t.Fatal("invalid season poster accepted")
	}

	c.Artwork = Artwork{}

	c.Playlists = &PlaylistPolicy{Enabled: true, Overrides: map[string]Selection{"bad": {Mode: "skip"}}}
	if c.validatePolicy() == nil {
		t.Fatal("invalid playlist accepted")
	}

	c.Playlists.Overrides = map[string]Selection{"PLabcdefghij": {Mode: "bad"}}
	if c.validatePolicy() == nil {
		t.Fatal("invalid override accepted")
	}
}

func TestPoliciesRoundTripWithoutChangingLegacyConfig(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := Create(file, filepath.Join(t.TempDir(), "state")); err != nil {
		t.Fatal(err)
	}

	m, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}

	s := m.Snapshot()
	selection := DefaultSelection()

	s.Config.Channels = []Channel{{ID: "UCSMOQeBJ2RAnuFungnQOxLg", URL: "https://www.youtube.com/channel/UCSMOQeBJ2RAnuFungnQOxLg", Limit: 50, Enabled: true, Indexing: &selection, Playlists: &PlaylistPolicy{Enabled: true}}}
	if err = m.Save(s.Config, s.Revision); err != nil {
		t.Fatal(err)
	}

	m, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}

	if m.Snapshot().Config.Channels[0].PlaylistSelection("PLabcdefghij") != selection {
		t.Fatal("policy lost on reload")
	}
}

func TestPolicySnapshotsDoNotPermitUnsavedMutation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := Create(file, filepath.Join(t.TempDir(), "state")); err != nil {
		t.Fatal(err)
	}

	m, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}

	s := m.Snapshot()
	selection := DefaultSelection()
	ref := strings.Repeat("b", 64) + ".jpg"

	s.Config.Channels = []Channel{{ID: "UCSMOQeBJ2RAnuFungnQOxLg", URL: "https://www.youtube.com/channel/UCSMOQeBJ2RAnuFungnQOxLg", Limit: 50, Indexing: &selection, Playlists: &PlaylistPolicy{Enabled: true, Overrides: map[string]Selection{"PLabcdefghij": {Mode: "all"}}}, Artwork: Artwork{Poster: ref, Seasons: map[string]string{"year:2026": ref}}}}
	if err = m.Save(s.Config, s.Revision); err != nil {
		t.Fatal(err)
	}

	s.Config.Channels[0].Indexing.Limit = 1

	got := m.Snapshot()
	if got.Config.Channels[0].Selection().Limit != 50 {
		t.Fatal("caller mutated committed settings")
	}

	got.Config.Channels[0].Indexing.Limit = 2
	got.Config.Channels[0].Playlists.Overrides["PLabcdefghij"] = Selection{Mode: "skip"}
	got.Config.Channels[0].Artwork.Seasons["year:2026"] = "bad"

	check := m.Snapshot().Config.Channels[0]
	if check.Selection().Limit != 50 || check.PlaylistSelection("PLabcdefghij").Mode != "all" || check.Artwork.Seasons["year:2026"] != ref {
		t.Fatal("snapshot mutated active settings")
	}
}
