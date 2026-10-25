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
	"reflect"
	"sync"
	"time"

	"duplex/internal/cache"
	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/diagnostics"
	"duplex/internal/library"
	"duplex/internal/media"
	"duplex/internal/plex"
	"duplex/internal/source"
)

// App coordinates catalogue discovery, media preparation, publication, and integration.
type App struct {
	runtimeUsed     map[string]time.Time
	prepareSlots    chan struct{}
	preparations    map[string]*preparationRun
	foreground      int
	bindings        map[string]*representationBinding
	httpClient      *http.Client
	metadata        map[string]metadataLease
	leases          map[string]time.Time
	prepareWake     chan struct{}
	scanWake        chan struct{}
	scanPending     bool
	Measurements    *diagnostics.Recorder
	Config          *config.Manager
	Catalog         *catalog.Store
	Cache           *cache.Store
	Source          source.SourceAdapter
	mount           *library.Mount
	mu              sync.RWMutex
	reps            map[string]media.Representation
	queued          map[string]bool
	attempted       map[string]time.Time
	jobs            chan string
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	original        config.Config
	toolError       string
	mountError      string
	plexError       string
	restart         bool
	appliedRevision string
	lastIntegration time.Time
	applied         config.Config
	cacheError      string
}

// Status is the compatibility diagnostic snapshot; Summary is intended for polling.
type Status struct {
	Ready           bool                      `json:"ready"`
	MountReady      bool                      `json:"mount_ready"`
	RestartRequired bool                      `json:"restart_required"`
	SourceError     string                    `json:"source_error,omitempty"`
	MountError      string                    `json:"mount_error,omitempty"`
	PlexError       string                    `json:"plex_error,omitempty"`
	ConfigError     string                    `json:"config_error,omitempty"`
	PendingJobs     int                       `json:"pending_jobs"`
	Cache           cache.Stats               `json:"cache"`
	Channels        []catalog.Channel         `json:"channels"`
	Videos          []catalog.Video           `json:"videos"`
	Jobs            []catalog.Job             `json:"jobs"`
	Config          config.Snapshot           `json:"configuration"`
	Measurements    []diagnostics.Measurement `json:"measurements,omitempty"`
}

// Open initializes storage and starts bounded workers under the supplied context.
func Open(ctx context.Context, m *config.Manager, adapter source.SourceAdapter, noMount bool) (*App, error) {
	c := m.Snapshot().Config
	for _, p := range []string{c.DataDir, c.IndexDir} {
		if e := os.MkdirAll(p, 0o700); e != nil {
			return nil, e
		}
	}
	db, e := catalog.OpenContext(ctx, filepath.Join(c.DataDir, "catalogue.sqlite"))
	if e != nil {
		return nil, e
	}
	store, e := cache.New(c.CacheDir, int64(c.CacheGiB)<<30)
	if e != nil {
		return nil, errors.Join(e, db.Close())
	}
	ctx, cancel := context.WithCancel(ctx)
	a := &App{Measurements: &diagnostics.Recorder{}, Config: m, Catalog: db, Cache: store, Source: adapter, reps: map[string]media.Representation{}, queued: map[string]bool{}, attempted: map[string]time.Time{}, jobs: make(chan string, 64), prepareWake: make(chan struct{}, 2), scanWake: make(chan struct{}, 1), metadata: map[string]metadataLease{}, leases: map[string]time.Time{}, ctx: ctx, cancel: cancel, original: c}
	if checker, ok := adapter.(interface{ Check(context.Context) error }); ok {
		checkCtx, stop := context.WithTimeout(ctx, 15*time.Second)
		e = checker.Check(checkCtx)
		stop()
		if e != nil {
			a.toolError = e.Error()
		}
	}
	a.Source = source.Observed{SourceAdapter: adapter, Record: a.Measurements.Add}
	if !noMount {
		a.mount, e = library.MountProvider(c.Mountpoint, catalogueFS{app: a})
		if e != nil {
			return nil, errors.Join(e, a.Close())
		}
	} else {
		a.mountError = "mount disabled; administration test mode"
	}
	if e = a.Publish(); e != nil {
		return nil, errors.Join(e, a.Close())
	}
	if e = a.Catalog.ResetPreparations(ctx, visibility(c)); e != nil {
		return nil, errors.Join(e, a.Close())
	}
	for range 2 {
		a.wg.Add(1)
		go a.preparationWorker(ctx)
	}
	a.wg.Add(1)
	go a.scanWorker(ctx)
	a.wg.Add(1)
	go a.assetWorker(ctx)
	a.wg.Add(1)
	go a.artworkWorker(ctx)
	for i := 0; i < c.Workers; i++ {
		a.wg.Add(1)
		go a.worker(ctx)
	}
	a.wg.Add(1)
	go a.schedule(ctx)
	return a, nil
}

// Close cancels workers before closing the mount, cache, and catalogue.
func (a *App) Close() error {
	a.cancel()
	a.wg.Wait()
	var mountErr error
	if a.mount != nil {
		mountErr = a.mount.Close()
	}
	e := a.Cache.Close()
	e2 := a.Catalog.Close()
	return errors.Join(mountErr, e, e2)
}

// Queue schedules one enabled channel and deduplicates pending refreshes.
func (a *App) Queue(id string) error {
	c := a.Config.Snapshot().Config
	found := false
	for _, ch := range c.Channels {
		if ch.ID == id && ch.Enabled {
			found = true
		}
	}
	if !found {
		return errors.New("channel is not followed or enabled")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.queued[id] {
		return nil
	}
	select {
	case a.jobs <- id:
		a.queued[id] = true
		return nil
	default:
		return errors.New("sync queue is full")
	}
}

func (a *App) schedule(ctx context.Context) {
	defer a.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	a.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.Config.Reload()
			a.reconcile(ctx)
		}
	}
}

func (a *App) reconcile(ctx context.Context) {
	snap := a.Config.Snapshot()
	c := snap.Config
	a.mu.Lock()
	changed := snap.Revision != a.appliedRevision
	previous := a.applied
	if changed && previous.MaxHeight != c.MaxHeight {
		for revision := range a.leases {
			a.leases[revision] = time.Time{}
		}
	}
	a.applied = c
	a.appliedRevision = snap.Revision
	a.restart = c.Listen != a.original.Listen || c.DataDir != a.original.DataDir || c.CacheDir != a.original.CacheDir || c.IndexDir != a.original.IndexDir || c.Mountpoint != a.original.Mountpoint || c.Workers != a.original.Workers
	a.mu.Unlock()
	resizeErr := a.Cache.Resize(int64(c.CacheGiB) << 30)
	a.mu.Lock()
	a.cacheError = ""
	if resizeErr != nil {
		a.cacheError = "Cache limit could not be applied; check local media storage"
	}
	a.mu.Unlock()
	a.scheduleChannels(c, previous, changed)
	if changed {
		a.operationFailure("publication", a.Publish())
	}
	if changed || time.Since(a.lastIntegration) >= time.Minute {
		a.lastIntegration = time.Now()
		ctx, stop := context.WithTimeout(ctx, 20*time.Second)
		a.operationFailure("integration health", a.integration(ctx, false))
		stop()
	}
}

func (a *App) scheduleChannels(c, previous config.Config, changed bool) {
	priorChannels := map[string]config.Channel{}
	for _, ch := range previous.Channels {
		priorChannels[ch.ID] = ch
	}
	for _, ch := range c.Channels {
		if !ch.Enabled {
			continue
		}
		a.mu.RLock()
		last := a.attempted[ch.ID]
		a.mu.RUnlock()
		settingsChanged := changed && (!reflect.DeepEqual(priorChannels[ch.ID], ch) || previous.MaxHeight != c.MaxHeight)
		if settingsChanged || last.IsZero() || time.Since(last) >= time.Duration(c.RefreshMinutes)*time.Minute {
			a.operationFailure("queue refresh", a.Queue(ch.ID))
		}
	}
}

func (a *App) worker(ctx context.Context) {
	defer a.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-a.jobs:
			a.mu.Lock()
			a.attempted[id] = time.Now()
			a.mu.Unlock()
			jobCtx, cancel := context.WithCancel(ctx)
			err := a.Sync(jobCtx, id)
			cancel()
			a.operationFailure("channel discovery", err)
			a.mu.Lock()
			delete(a.queued, id)
			a.mu.Unlock()
		}
	}
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
	if err := selection.ValidateUploads(); err != nil {
		return err
	}
	ctx, stop := context.WithTimeout(ctx, 2*time.Minute)
	defer stop()
	ch, e := a.Source.ResolveChannel(ctx, raw)
	if e != nil {
		return e
	}
	snap := a.Config.Snapshot()
	if snap.Revision != expected {
		return config.ErrConflict
	}
	for _, c := range snap.Config.Channels {
		if c.ID == ch.ID {
			return errors.New("channel is already followed")
		}
	}
	url := "https://www.youtube.com/channel/" + ch.ID
	snap.Config.Channels = append(snap.Config.Channels, config.Channel{ID: ch.ID, URL: url, Limit: 50, Enabled: true, Indexing: &selection, Playlists: &config.PlaylistPolicy{Enabled: include}})
	if e = a.Config.Save(snap.Config, expected); e != nil {
		return e
	}
	if e = a.Catalog.EnsureChannel(ctx, ch.ID, ch.Title); e != nil {
		return errors.New("settings saved; catalogue channel initialization failed")
	}
	return a.Queue(ch.ID)
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

func (a *App) plexClient() (*plex.Client, error) {
	p := a.Config.Snapshot().Config.Plex
	if p.URL == "" {
		return nil, errors.New("plex connection is not configured")
	}
	token := ""
	if p.TokenFile != "" {
		b, e := os.ReadFile(p.TokenFile)
		if e != nil {
			return nil, errors.New("cannot read Plex token file")
		}
		token = string(b)
	}
	return plex.New(p.URL, token)
}

// Scan checks the mount and Plex prerequisites before requesting library discovery.
func (a *App) Scan(ctx context.Context) error {
	defer a.Measurements.Begin("plex.scan")()
	return a.integration(ctx, true)
}

func (a *App) integration(ctx context.Context, scan bool) (err error) {
	if a.mount == nil {
		return nil
	}
	defer func() { a.setPlexError(err) }()
	if !a.mount.Ready() {
		a.mu.Lock()
		a.mountError = "Library mount is unavailable; scans are disabled"
		a.mu.Unlock()
		return errors.New("library mount is unavailable")
	}
	connection := a.Config.Snapshot().Config.Plex
	client, err := a.plexClient()
	if err != nil {
		return err
	}
	if _, err = client.Check(ctx); err != nil {
		return err
	}
	if err = client.LibraryHealth(ctx, connection.Section); err != nil {
		return err
	}
	if !scan {
		return nil
	}
	if err = client.Scan(ctx, connection.Section); err != nil {
		return err
	}
	return a.bindPlex(ctx, client, connection)
}

func (a *App) bindPlex(ctx context.Context, client *plex.Client, connection config.Plex) error {
	return client.WalkBindings(ctx, connection.Section, func(bindings []plex.Binding) error {
		for _, binding := range bindings {
			if err := a.bindPlexItem(ctx, binding, connection); err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *App) bindPlexItem(ctx context.Context, binding plex.Binding, connection config.Plex) error {
	if occurrence := plex.OccurrenceID(binding.GUID); occurrence != "" {
		if err := a.Catalog.BindOccurrence(ctx, occurrence, connection.URL, binding.Key, binding.GUID); err != nil {
			return err
		}
	}
	if id := plex.SourceID(binding.GUID); id != "" {
		return a.Catalog.Bind(ctx, id, connection.URL, binding.Key, binding.GUID)
	}
	return nil
}

func (a *App) setPlexError(e error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.plexError = ""
	if e != nil {
		a.plexError = e.Error()
	}
}

// Status is the compatibility diagnostic snapshot; Summary is intended for polling.
func (a *App) Status(ctx context.Context) (Status, error) {
	s := Status{Config: a.Config.Snapshot(), Cache: a.Cache.Stats(), Measurements: a.Measurements.Snapshot()}
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
