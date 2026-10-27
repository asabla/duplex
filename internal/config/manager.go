package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Snapshot pairs cloned settings with the file revision used for a stale-save check.
type Snapshot struct {
	Config   Config    `json:"config"`
	Revision string    `json:"revision"`
	Error    string    `json:"error,omitempty"`
	LoadedAt time.Time `json:"loaded_at"`
}

// Manager serializes valid YAML edits and retains the last valid external configuration.
type Manager struct {
	fileMu  sync.Mutex // Serializes file operations without blocking filesystem callbacks.
	mu      sync.Mutex
	path    string
	current Snapshot
}

func revision(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Open loads an existing valid YAML file.
func Open(path string) (*Manager, error) {
	b, e := os.ReadFile(path) //nolint:gosec // G304: The operator selects the single writable configuration file at startup.
	if e != nil {
		return nil, e
	}

	c, e := Decode(b)
	if e != nil {
		return nil, e
	}

	return &Manager{path: path, current: Snapshot{Config: c, Revision: revision(b), LoadedAt: time.Now().UTC()}}, nil
}

// Snapshot pairs cloned settings with the file revision used for a stale-save check.
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := m.current
	s.Config = clone(s.Config)

	return s
}

func clone(c Config) Config {
	s := c

	s.Channels = append([]Channel{}, c.Channels...)
	for i := range s.Channels {
		ch := &s.Channels[i]

		ch.Unlisted = append([]string{}, ch.Unlisted...)

		ch.VideoOverrides = maps.Clone(ch.VideoOverrides)
		if ch.Content != nil {
			content := *ch.Content
			ch.Content = &content
		}

		if ch.Indexing != nil {
			p := *ch.Indexing
			ch.Indexing = &p
		}

		if ch.Playlists != nil {
			p := *ch.Playlists
			p.Overrides = maps.Clone(p.Overrides)
			ch.Playlists = &p
		}

		ch.Artwork.Seasons = maps.Clone(ch.Artwork.Seasons)
	}

	return s
}

// Reload applies a valid external edit and records an error for an invalid edit.
func (m *Manager) Reload() bool {
	m.fileMu.Lock()
	defer m.fileMu.Unlock()

	b, e := os.ReadFile(m.path)
	if e != nil {
		m.mu.Lock()
		m.current.Error = "cannot read configuration file"
		m.mu.Unlock()

		return false
	}

	r := revision(b)
	if r == m.Snapshot().Revision {
		m.mu.Lock()
		if m.current.Error == "cannot read configuration file" {
			m.current.Error = ""
		}

		m.mu.Unlock()

		return false
	}

	// Storage validation can call the mounted filesystem, whose provider reads
	// Snapshot. Never hold the snapshot mutex across this operation.
	c, e := Decode(b)

	m.mu.Lock()
	defer m.mu.Unlock()

	if e != nil {
		m.current.Error = "External configuration is invalid; last valid settings remain active. " + e.Error()
		m.current.Revision = r

		return false
	}

	m.current = Snapshot{Config: c, Revision: r, LoadedAt: time.Now().UTC()}

	return true
}

// Save validates settings and atomically replaces the expected file revision.
func (m *Manager) Save(c Config, expected string) error {
	m.fileMu.Lock()
	defer m.fileMu.Unlock()

	if e := c.Validate(); e != nil {
		return e
	}

	b, e := os.ReadFile(m.path)
	if e != nil {
		return errors.New("cannot read configuration file")
	}

	if revision(b) != expected {
		return ErrConflict
	}

	b, e = yaml.Marshal(c)
	if e != nil {
		return e
	}

	if e = AtomicWrite(m.path, b, 0o600); e != nil {
		return e
	}

	m.mu.Lock()
	m.current = Snapshot{Config: clone(c), Revision: revision(b), LoadedAt: time.Now().UTC()}
	m.mu.Unlock()

	return nil
}
