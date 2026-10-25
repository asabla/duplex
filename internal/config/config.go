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
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

var ErrConflict = errors.New("configuration changed; reload before saving")

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
type Plex struct {
	URL       string `yaml:"url" json:"url"`
	Section   string `yaml:"section" json:"section"`
	TokenFile string `yaml:"token_file" json:"token_file"`
}
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

func Default(base string) Config {
	return Config{Version: 1, Listen: "127.0.0.1:8765", DataDir: filepath.Join(base, "data"), CacheDir: filepath.Join(base, "media"), IndexDir: filepath.Join(base, "indexes"), Mountpoint: filepath.Join(base, "library", "media"), CacheGiB: 50, RefreshMinutes: 30, Workers: 2, MaxHeight: 2160, Channels: []Channel{}}
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return errors.New("unsupported configuration version")
	}
	if _, _, e := net.SplitHostPort(c.Listen); e != nil {
		return errors.New("listen must contain host and port")
	}
	paths := []string{c.DataDir, c.CacheDir, c.IndexDir, c.Mountpoint}
	for i, p := range paths {
		if !filepath.IsAbs(p) || filepath.Clean(p) == "/" {
			return errors.New("storage and mount paths must be absolute dedicated directories")
		}
		for j := 0; j < i; j++ {
			rel, e := filepath.Rel(paths[j], p)
			rev, _ := filepath.Rel(p, paths[j])
			if e == nil && (rel == "." || !outside(rel) || !outside(rev)) {
				return errors.New("storage and mount directories must be separate")
			}
		}
	}
	if c.CacheGiB < 1 || c.CacheGiB > 1048576 || c.RefreshMinutes < 1 || c.RefreshMinutes > 10080 || c.Workers < 1 || c.Workers > 4 || c.MaxHeight < 144 || c.MaxHeight > 2160 {
		return errors.New("invalid cache, refresh, worker, or quality setting")
	}
	if c.Plex.URL != "" {
		u, e := url.Parse(c.Plex.URL)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("Plex URL must be an HTTP(S) origin")
		}
	}
	seen := map[string]bool{}
	for _, ch := range c.Channels {
		if !validID(ch.ID) || seen[ch.ID] {
			return errors.New("invalid or duplicate channel settings")
		}
		if err := ch.validatePolicy(); err != nil {
			return err
		}
		seen[ch.ID] = true
		for _, id := range ch.Unlisted {
			if len(id) != 11 {
				return errors.New("unlisted video IDs must contain 11 URL-safe characters")
			}
			for _, r := range id {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
					return errors.New("invalid unlisted video ID")
				}
			}
		}
		if _, e := ChannelURL(ch.URL); e != nil {
			return e
		}
	}
	return nil
}

func outside(p string) bool {
	return p == ".." || len(p) > 3 && p[:3] == ".."+string(filepath.Separator)
}

func validID(s string) bool {
	if len(s) != 24 || s[:2] != "UC" {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func ChannelURL(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Scheme != "https" || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("enter an HTTPS YouTube channel URL")
	}
	if u.Hostname() != "www.youtube.com" && u.Hostname() != "youtube.com" {
		return "", errors.New("enter an HTTPS YouTube channel URL")
	}
	parts := bytes.Split([]byte(u.Path), []byte("/"))
	if len(parts) < 2 || len(parts) > 4 {
		return "", errors.New("enter a channel URL, not a video or playlist")
	}
	p := string(parts[1])
	if p == "channel" {
		if len(parts) < 3 || !validID(string(parts[2])) {
			return "", errors.New("invalid channel ID")
		}
	} else if len(p) < 2 || p[0] != '@' {
		return "", errors.New("use a channel ID or handle URL")
	}
	if p != "channel" && len(parts) > 2 && string(parts[2]) != "" && string(parts[2]) != "videos" && string(parts[2]) != "streams" {
		return "", errors.New("unsupported channel URL")
	}
	if p == "channel" {
		return "https://www.youtube.com/channel/" + string(parts[2]), nil
	}
	return "https://www.youtube.com/" + p, nil
}

func Decode(b []byte) (Config, error) {
	var c Config
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if e := d.Decode(&c); e != nil {
		return c, errors.New("invalid YAML configuration")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, errors.New("configuration must contain one YAML document")
	}
	return c, c.Validate()
}
func revision(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type Snapshot struct {
	Config   Config    `json:"config"`
	Revision string    `json:"revision"`
	Error    string    `json:"error,omitempty"`
	LoadedAt time.Time `json:"loaded_at"`
}
type Manager struct {
	mu      sync.Mutex
	path    string
	current Snapshot
}

func Open(path string) (*Manager, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	c, e := Decode(b)
	if e != nil {
		return nil, e
	}
	return &Manager{path: path, current: Snapshot{Config: c, Revision: revision(b), LoadedAt: time.Now().UTC()}}, nil
}

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

func AtomicWrite(path string, b []byte, mode os.FileMode) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".duplex-write-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	if closeErr := f.Close(); e == nil {
		e = closeErr
	}
	ok = true
	if e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

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
