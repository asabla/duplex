package config

import (
	"slices"
	"time"
)

// LibraryChannel is the visibility policy required by filesystem reads. It
// excludes resolver settings, artwork, and potentially large video override maps.
type LibraryChannel struct {
	ID               string
	Enabled          bool
	Playlists        bool
	SkippedPlaylists []string
}

// indexChannels runs with the manager mutex held after a configuration change.
func (m *Manager) indexChannels() {
	m.channels = make(map[string]int, len(m.current.Config.Channels))

	m.libraryChannels = make(map[string]LibraryChannel, len(m.current.Config.Channels))
	for i, ch := range m.current.Config.Channels {
		m.channels[ch.ID] = i

		policy := LibraryChannel{ID: ch.ID, Enabled: ch.Enabled}
		if ch.Playlists != nil && ch.Playlists.Enabled {
			policy.Playlists = true
			for id, selection := range ch.Playlists.Overrides {
				if selection.Mode == "skip" {
					policy.SkippedPlaylists = append(policy.SkippedPlaylists, id)
				}
			}

			slices.Sort(policy.SkippedPlaylists)
		}

		m.libraryChannels[ch.ID] = policy
	}
}

// LibraryChannel returns an isolated visibility policy without copying media
// selection maps. Skip lists are derived once per applied configuration.
func (m *Manager) LibraryChannel(id string) (LibraryChannel, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.channels == nil {
		m.indexChannels()
	}

	policy, ok := m.libraryChannels[id]
	policy.SkippedPlaylists = slices.Clone(policy.SkippedPlaylists)

	return policy, ok
}

// LibraryChannels preserves configured channel order for root membership reads.
func (m *Manager) LibraryChannels() []LibraryChannel {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.channels == nil {
		m.indexChannels()
	}

	channels := make([]LibraryChannel, 0, len(m.current.Config.Channels))
	for _, ch := range m.current.Config.Channels {
		policy := m.libraryChannels[ch.ID]
		policy.SkippedPlaylists = slices.Clone(policy.SkippedPlaylists)
		channels = append(channels, policy)
	}

	return channels
}

// LoadedAt reads the applied configuration timestamp without copying settings.
func (m *Manager) LoadedAt() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.current.LoadedAt
}
