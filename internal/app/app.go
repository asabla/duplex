// Package app contains services shared by the JSON and HTMX handlers.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"duplex/internal/cache"
	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/diagnostics"
	"duplex/internal/library"
	"duplex/internal/media"
	"duplex/internal/source"
)

// App coordinates catalogue discovery, media preparation, publication, and integration.
type App struct {
	schedulerState
	preparationState
	representationState
	libraryState

	httpClient       *http.Client
	Measurements     *diagnostics.Recorder
	Config           *config.Manager
	Catalog          *catalog.Store
	Cache            *cache.Store
	Source           source.SourceAdapter
	mount            *library.Mount
	mu               sync.RWMutex // Retained across state groups to preserve cross-operation atomicity.
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	original         config.Config
	toolError        string
	mountError       string
	plexError        string
	plexBindingError string
	restart          bool
	cacheError       string
}

// Status is the compatibility diagnostic snapshot; Summary is intended for polling.
type Status struct {
	Ready           bool                      `json:"ready"`
	MountReady      bool                      `json:"mount_ready"`
	RestartRequired bool                      `json:"restart_required"`
	SourceError     string                    `json:"source_error,omitempty"`
	MountError      string                    `json:"mount_error,omitempty"`
	PlexError       string                    `json:"plex_error,omitempty"`
	MetadataError   string                    `json:"metadata_error,omitempty"`
	MetadataPending int                       `json:"metadata_pending"`
	ConfigError     string                    `json:"config_error,omitempty"`
	PendingJobs     int                       `json:"pending_jobs"`
	Cache           cache.Stats               `json:"cache"`
	Channels        []catalog.Channel         `json:"channels"`
	Videos          []catalog.Video           `json:"videos"`
	Jobs            []catalog.Job             `json:"jobs"`
	Config          config.Snapshot           `json:"configuration"`
	Measurements    []diagnostics.Measurement `json:"measurements,omitempty"`
	Counters        []diagnostics.Counter     `json:"counters,omitempty"`
}

// AddChannel preserves the original numeric-limit API. New clients can use Follow.
func (a *App) AddChannel(ctx context.Context, raw string, limit int, expected string) error {
	if limit == 0 {
		limit = 50
	}
	return a.Follow(ctx, raw, config.Selection{Mode: "latest", Limit: limit}, true, expected)
}

// Follow creates explicit upload and playlist defaults in the authoritative YAML.
func (a *App) Follow(ctx context.Context, raw string, selection config.Selection, include bool, expected string) error {
	_, err := a.FollowChannel(ctx, raw, selection, include, expected)
	return err
}

// FollowChannel follows a channel and returns its resolved identity without another source request.
func (a *App) FollowChannel(ctx context.Context, raw string, selection config.Selection, include bool, expected string) (string, error) {
	if err := selection.ValidateUploads(); err != nil {
		return "", err
	}
	if a.Config.Snapshot().Revision != expected {
		return "", config.ErrConflict
	}
	ctx, stop := context.WithTimeout(ctx, 2*time.Minute)
	defer stop()
	ch, e := a.Source.ResolveChannel(ctx, raw)
	if e != nil {
		return "", e
	}
	snap := a.Config.Snapshot()
	if snap.Revision != expected {
		return "", config.ErrConflict
	}
	for _, c := range snap.Config.Channels {
		if c.ID == ch.ID {
			return "", errors.New("channel is already followed")
		}
	}
	url := "https://www.youtube.com/channel/" + ch.ID
	snap.Config.Channels = append(snap.Config.Channels, config.Channel{ID: ch.ID, URL: url, Limit: 50, Enabled: true, Indexing: &selection, Playlists: &config.PlaylistPolicy{Enabled: include}})
	if e = a.Config.Save(snap.Config, expected); e != nil {
		return "", e
	}
	if e = a.Catalog.EnsureChannel(ctx, ch.ID, ch.Title); e != nil {
		return "", errors.New("settings saved; catalogue channel initialization failed")
	}
	return ch.ID, a.Queue(ch.ID)
}

// RemoveChannel updates YAML and removes the channel from the published library.
func (a *App) RemoveChannel(id, expected string) error {
	s := a.Config.Snapshot()
	channels := []config.Channel{}
	found := false
	for _, ch := range s.Config.Channels {
		if ch.ID == id {
			found = true
		} else {
			channels = append(channels, ch)
		}
	}
	if !found {
		return errors.New("channel is not followed")
	}
	s.Config.Channels = channels
	if e := a.Config.Save(s.Config, expected); e != nil {
		return e
	}
	return a.Publish()
}

func fallbackReason(e error) string {
	var pathError *os.PathError
	switch {
	case errors.Is(e, context.DeadlineExceeded):
		return "A higher-quality index exceeded its work limit"
	case errors.Is(e, media.ErrUnsupported):
		return "A higher-quality format is unsupported"
	case errors.Is(e, media.ErrExpired):
		return "A higher-quality source locator was rejected"
	case errors.Is(e, cache.ErrFull):
		return "The media cache has no reclaimable space"
	case errors.As(e, &pathError):
		return "A higher-quality index could not be saved"
	default:
		return "A higher-quality source could not be verified"
	}
}

// Publish makes current durable state available through the database-backed mount.
// Directory and attribute reads use bounded queries with no catalogue snapshot.
func (a *App) Publish() error {
	defer a.Measurements.Begin("publish")()
	return a.ctx.Err()
}

// Status is the compatibility diagnostic snapshot; Summary is intended for polling.
func (a *App) Status(ctx context.Context) (Status, error) {
	s := Status{Config: a.Config.Snapshot(), Cache: a.Cache.Stats(), Measurements: a.Measurements.Snapshot(), Counters: a.Measurements.Counters()}
	var e error
	if s.Channels, e = a.Catalog.Channels(ctx); e != nil {
		return s, e
	}
	if s.Videos, e = a.Catalog.Videos(ctx); e != nil {
		return s, e
	}
	if s.Jobs, e = a.Catalog.Jobs(ctx); e != nil {
		return s, e
	}
	if s.MetadataPending, s.MetadataError, e = a.Catalog.ShowRefreshStatus(ctx); e != nil {
		return s, e
	}
	a.mu.RLock()
	s.SourceError, s.MountError, s.PlexError, s.RestartRequired, s.PendingJobs = a.toolError, a.mountError, a.plexError, a.restart, len(a.queued)
	cacheError := a.cacheError
	s.MountReady = a.mount != nil && a.mount.Ready()
	a.statusVideos(&s)
	a.mu.RUnlock()
	s.ConfigError = s.Config.Error
	if cacheError != "" {
		s.ConfigError = cacheError
	}
	s.Ready = s.MountReady && s.SourceError == "" && s.PlexError == "" && !s.RestartRequired
	return s, nil
}

// statusVideos requires a.mu to be held by the caller.
func (a *App) statusVideos(s *Status) {
	enabled := map[string]bool{}
	for _, ch := range s.Config.Config.Channels {
		enabled[ch.ID] = ch.Enabled
	}
	for i := range s.Videos {
		v := &s.Videos[i]
		if !enabled[v.ChannelID] {
			v.Exposed = false
		}
	}
}

// localAsset is bounded metadata, not an evictable media or transcode file.
type localAsset struct {
	file string
	size int64
}

func (r localAsset) ID() string  { return filepath.Base(r.file) }
func (r localAsset) Size() int64 { return r.size }
func (r localAsset) ReadAt(ctx context.Context, p []byte, o int64) (n int, result error) {
	if e := ctx.Err(); e != nil {
		return 0, e
	}
	f, e := os.Open(r.file)
	if e != nil {
		return 0, e
	}
	defer func() { result = errors.Join(result, f.Close()) }()
	return f.ReadAt(p, o)
}

func (a *App) saveAsset(id, suffix string, b []byte) (library.Entry, error) {
	if !assetID.MatchString(id) {
		return library.Entry{}, errors.New("invalid asset identity")
	}
	dir := filepath.Join(a.original.DataDir, "assets")
	if e := os.MkdirAll(dir, 0o700); e != nil {
		return library.Entry{}, e
	}
	h := sha256.Sum256(b)
	file := filepath.Join(dir, id+"-"+hex.EncodeToString(h[:])+suffix)
	if e := config.AtomicWrite(file, b, 0o600); e != nil {
		return library.Entry{}, e
	}
	return library.Entry{Path: suffix, Media: localAsset{file: file, size: int64(len(b))}}, nil
}

func fetchAsset(ctx context.Context, raw string) (data []byte, result error) {
	if !source.AllowedURL(raw) {
		return nil, errors.New("unsupported asset host")
	}
	req, e := httpRequest(ctx, raw)
	if e != nil {
		return nil, e
	}
	r, e := source.HTTPClient().Do(req)
	if e != nil {
		return nil, errors.New("asset unavailable")
	}
	defer func() { result = errors.Join(result, r.Body.Close()) }()
	if r.StatusCode != 200 {
		return nil, errors.New("asset unavailable")
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 8<<20+1))
	if e != nil || len(b) > 8<<20 {
		return nil, errors.New("asset exceeds metadata size limit")
	}
	return b, nil
}
