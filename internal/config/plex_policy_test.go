package config

import (
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPlexPlaybackAndAutomationDefaultsRoundTripAndClone(t *testing.T) {
	p := Plex{URL: "http://plex.test:32400"}
	if !p.ProtectPlayback() || !p.UpdatesEnabled() || (Plex{}).ProtectPlayback() {
		t.Fatal("Plex defaults changed")
	}

	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := Create(file, filepath.Join(t.TempDir(), "state")); err != nil {
		t.Fatal(err)
	}

	manager, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}

	snapshot := manager.Snapshot()
	protection, updates := true, false
	snapshot.Config.Plex = Plex{URL: p.URL, PlaybackProtection: &protection, AutomaticUpdates: &updates}

	if err = manager.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}

	protection, updates = false, true

	snapshot = manager.Snapshot()
	if !snapshot.Config.Plex.ProtectPlayback() || snapshot.Config.Plex.UpdatesEnabled() {
		t.Fatal("save retained caller-owned option pointers")
	}

	*snapshot.Config.Plex.PlaybackProtection = false
	*snapshot.Config.Plex.AutomaticUpdates = true

	if p = manager.Snapshot().Config.Plex; !p.ProtectPlayback() || p.UpdatesEnabled() {
		t.Fatal("snapshot leaked mutable Plex options")
	}

	reopened, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}

	if p = reopened.Snapshot().Config.Plex; !p.ProtectPlayback() || p.UpdatesEnabled() {
		t.Fatal("Plex options did not survive restart")
	}

	for _, field := range []string{"playback_protection", "automatic_updates"} {
		var invalid Plex
		if err = yaml.Unmarshal([]byte(field+": invalid\n"), &invalid); err == nil {
			t.Fatal("non-boolean Plex option accepted", field)
		}
	}
}
