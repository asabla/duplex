package config

import (
	"path/filepath"
	"testing"
)

func TestContentRulesValidateAndSnapshotsRemainIndependent(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := Create(file, filepath.Join(t.TempDir(), "state")); err != nil {
		t.Fatal(err)
	}

	m, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}

	snapshot := m.Snapshot()
	ch := Channel{ID: "UCaaaaaaaaaaaaaaaaaaaaaa", URL: "https://www.youtube.com/channel/UCaaaaaaaaaaaaaaaaaaaaaa", Limit: 50, Enabled: true, Content: &ContentPolicy{Uploads: Selection{Mode: "latest", Limit: 20}, Shorts: Selection{Mode: "skip"}, Replays: Selection{Mode: "all"}}, VideoOverrides: map[string]string{"video000001": "keep"}}

	snapshot.Config.Channels = []Channel{ch}
	if err = m.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}

	ch.Content.Uploads.Limit = 1
	ch.VideoOverrides["video000001"] = "exclude"

	got := m.Snapshot()
	if got.Config.Channels[0].Content.Uploads.Limit != 20 || got.Config.Channels[0].VideoOverrides["video000001"] != "keep" {
		t.Fatal("save retained caller-owned maps or pointers")
	}

	got.Config.Channels[0].Content.Replays.Mode = "skip"
	delete(got.Config.Channels[0].VideoOverrides, "video000001")

	if current := m.Snapshot().Config.Channels[0]; current.Content.Replays.Mode != "all" || len(current.VideoOverrides) != 1 {
		t.Fatal("snapshot edits mutated settings")
	}

	for _, mode := range []string{"inherit", "invalid", ""} {
		bad := m.Snapshot().Config.Channels[0]

		bad.Content.Shorts.Mode = mode
		if bad.ValidateSelection() == nil {
			t.Fatal("invalid content mode accepted", mode)
		}
	}

	for id, mode := range map[string]string{"bad": "keep", "video000001": "unknown"} {
		bad := m.Snapshot().Config.Channels[0]

		bad.VideoOverrides = map[string]string{id: mode}
		if bad.ValidateSelection() == nil {
			t.Fatal("invalid override accepted", id, mode)
		}
	}

	if ch.ContentSelection("unknown").Mode != "skip" {
		t.Fatal("unknown content was guessed")
	}
}
