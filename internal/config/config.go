// Package config keeps the YAML file as the settings authority.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrConflict means the file changed after the caller read its revision.
var ErrConflict = errors.New("configuration changed; reload before saving")

// Channel stores following, collection selection, and artwork policies by stable ID.
type Channel struct {
	ID        string          `yaml:"id" json:"id"`
	URL       string          `yaml:"url" json:"url"`
	Limit     int             `yaml:"limit" json:"limit"`
	Enabled   bool            `yaml:"enabled" json:"enabled"`
	Unlisted  []string        `yaml:"unlisted_videos,omitempty" json:"unlisted_videos,omitempty"`
	Indexing  *Selection      `yaml:"indexing,omitempty" json:"indexing,omitempty"`
	Playlists *PlaylistPolicy `yaml:"playlists,omitempty" json:"playlists,omitempty"`
	Artwork   Artwork         `yaml:"artwork,omitempty" json:"artwork,omitempty"`
}

// Plex identifies the dedicated library and an external protected token file.
type Plex struct {
	URL       string `yaml:"url" json:"url"`
	Section   string `yaml:"section" json:"section"`
	TokenFile string `yaml:"token_file" json:"token_file"`
}

// Config is the authoritative YAML schema for one installation.
type Config struct {
	Version        int       `yaml:"version" json:"version"`
	Listen         string    `yaml:"listen" json:"listen"`
	DataDir        string    `yaml:"data_dir" json:"data_dir"`
	CacheDir       string    `yaml:"cache_dir" json:"cache_dir"`
	IndexDir       string    `yaml:"index_dir" json:"index_dir"`
	Mountpoint     string    `yaml:"mountpoint" json:"mountpoint"`
	CacheGiB       int       `yaml:"cache_gib" json:"cache_gib"`
	RefreshMinutes int       `yaml:"refresh_minutes" json:"refresh_minutes"`
	Workers        int       `yaml:"workers" json:"workers"`
	MaxHeight      int       `yaml:"max_height" json:"max_height"`
	Plex           Plex      `yaml:"plex" json:"plex"`
	Channels       []Channel `yaml:"channels" json:"channels"`
}

// Default returns household settings using separate durable and evictable directories.
func Default(base string) Config {
	return Config{Version: 1, Listen: "127.0.0.1:8765", DataDir: filepath.Join(base, "data"), CacheDir: filepath.Join(base, "media"), IndexDir: filepath.Join(base, "indexes"), Mountpoint: filepath.Join(base, "library", "media"), CacheGiB: 50, RefreshMinutes: 30, Workers: 2, MaxHeight: 2160, Channels: []Channel{}}
}

// Validate checks storage isolation, service limits, and channel policies.
func (c Config) Validate() error {
	if c.Version != 1 {
		return errors.New("unsupported configuration version")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return errors.New("listen must contain host and port")
	}
	if err := validateStorage([]string{c.DataDir, c.CacheDir, c.IndexDir, c.Mountpoint}); err != nil {
		return err
	}
	if c.CacheGiB < 1 || c.CacheGiB > 1048576 || c.RefreshMinutes < 1 || c.RefreshMinutes > 10080 || c.Workers < 1 || c.Workers > 4 || c.MaxHeight < 144 || c.MaxHeight > 2160 {
		return errors.New("invalid cache, refresh, worker, or quality setting")
	}
	if err := validatePlex(c.Plex); err != nil {
		return err
	}
	return validateChannels(c.Channels)
}

func validateStorage(paths []string) error {
	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return errors.New("storage and mount paths must be absolute dedicated directories")
		}
		canonical, err := resolveExistingPath(path)
		if err != nil {
			return err
		}
		if canonical == "/" {
			return errors.New("storage and mount paths must be dedicated directories")
		}
		for _, previous := range resolved {
			relative, err := filepath.Rel(previous, canonical)
			if err != nil {
				return err
			}
			reverse, err := filepath.Rel(canonical, previous)
			if err != nil {
				return err
			}
			if !outside(relative) || !outside(reverse) {
				return errors.New("storage and mount directories must be separate")
			}
		}
		resolved = append(resolved, canonical)
	}
	return nil
}

func resolveExistingPath(path string) (string, error) {
	suffix := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		suffix = append(suffix, filepath.Base(path))
		path = parent
	}
}

func validatePlex(p Plex) error {
	if p.URL == "" {
		return nil
	}
	u, err := url.Parse(p.URL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("connection to Plex must use an HTTP(S) origin")
	}
	return nil
}

func validateChannels(channels []Channel) error {
	seen := map[string]bool{}
	for _, ch := range channels {
		if !validID(ch.ID) || seen[ch.ID] {
			return errors.New("invalid or duplicate channel settings")
		}
		if err := ch.validatePolicy(); err != nil {
			return err
		}
		seen[ch.ID] = true
		for _, id := range ch.Unlisted {
			if !validVideoID(id) {
				return errors.New("unlisted video IDs must contain 11 URL-safe characters")
			}
		}
		if _, err := ChannelURL(ch.URL); err != nil {
			return err
		}
	}
	return nil
}

func validVideoID(id string) bool {
	if len(id) != 11 {
		return false
	}
	for _, r := range id {
		if !urlCharacter(r) {
			return false
		}
	}
	return true
}

func urlCharacter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
}

func outside(p string) bool {
	return p == ".." || len(p) > 3 && p[:3] == ".."+string(filepath.Separator)
}

func validID(s string) bool {
	if len(s) != 24 || s[:2] != "UC" {
		return false
	}
	for _, r := range s {
		valid := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !valid {
			return false
		}
	}
	return true
}

// ChannelURL validates and canonicalizes a supported public YouTube channel URL.
func ChannelURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || !channelOrigin(u) {
		return "", errors.New("enter an HTTPS YouTube channel URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", errors.New("enter a channel URL")
	}
	if parts[0] == "channel" {
		return channelIDURL(parts)
	}
	return channelHandleURL(parts)
}

func channelOrigin(u *url.URL) bool {
	if u.User != nil || u.Scheme != "https" || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "www.youtube.com" || host == "youtube.com"
}

func channelHandleURL(parts []string) (string, error) {
	if len(parts[0]) < 2 || !strings.HasPrefix(parts[0], "@") {
		return "", errors.New("use a channel ID or handle URL")
	}
	if len(parts) > 2 || (len(parts) == 2 && !channelTab(parts[1])) {
		return "", errors.New("unsupported channel URL")
	}
	canonical := url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/" + parts[0]}
	return canonical.String(), nil
}

func channelIDURL(parts []string) (string, error) {
	if len(parts) < 2 || !validID(parts[1]) {
		return "", errors.New("invalid channel ID")
	}
	if len(parts) > 3 || (len(parts) == 3 && !channelTab(parts[2])) {
		return "", errors.New("unsupported channel URL")
	}
	return "https://www.youtube.com/channel/" + parts[1], nil
}

func channelTab(tab string) bool {
	switch tab {
	case "videos", "streams", "shorts", "playlists":
		return true
	default:
		return false
	}
}

// Decode reads exactly one YAML configuration and validates all settings.
func Decode(b []byte) (Config, error) {
	var c Config
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if e := d.Decode(&c); e != nil {
		return c, errors.New("invalid YAML configuration")
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return c, errors.New("configuration must contain one YAML document")
	}
	return c, c.Validate()
}
func revision(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Snapshot pairs cloned settings with the file revision used for a stale-save check.
type Snapshot struct {
	Config   Config    `json:"config"`
	Revision string    `json:"revision"`
	Error    string    `json:"error,omitempty"`
	LoadedAt time.Time `json:"loaded_at"`
}

// Manager serializes valid YAML edits and retains the last valid external configuration.
type Manager struct {
	mu      sync.Mutex
	path    string
	current Snapshot
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
	m.mu.Lock()
	defer m.mu.Unlock()
	b, e := os.ReadFile(m.path)
	if e != nil {
		m.current.Error = "cannot read configuration file"
		return false
	}
	r := revision(b)
	if r == m.current.Revision {
		if m.current.Error == "cannot read configuration file" {
			m.current.Error = ""
		}
		return false
	}
	c, e := Decode(b)
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
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.current = Snapshot{Config: clone(c), Revision: revision(b), LoadedAt: time.Now().UTC()}
	return nil
}

// AtomicWrite publishes a synced temporary file and then syncs its parent directory.
func AtomicWrite(path string, b []byte, mode os.FileMode) (result error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".duplex-write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// Create writes initial settings without replacing an existing configuration.
func Create(path, base string) error {
	if _, e := os.Lstat(path); e == nil {
		return fmt.Errorf("configuration already exists")
	}
	if e := os.MkdirAll(filepath.Dir(path), 0o700); e != nil {
		return e
	}
	b, e := yaml.Marshal(Default(base))
	if e != nil {
		return e
	}
	return AtomicWrite(path, b, 0o600)
}
