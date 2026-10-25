// Package config keeps the YAML file as the settings authority.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

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
