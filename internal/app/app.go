// Package app contains services shared by the JSON and HTMX handlers.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
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

type App struct {
	Measurements    *diagnostics.Recorder
	Config          *config.Manager
	Catalog         *catalog.Store
	Cache           *cache.Store
	Source          source.SourceAdapter
	mount           *library.Mount
	mu              sync.RWMutex
	publishMu       sync.Mutex
	reps            map[string]media.Representation
	assets          map[string][]library.Entry
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

func Open(ctx context.Context, m *config.Manager, adapter source.SourceAdapter, noMount bool) (*App, error) {
	c := m.Snapshot().Config
	for _, p := range []string{c.DataDir, c.IndexDir} {
		if e := os.MkdirAll(p, 0o700); e != nil {
			return nil, e
		}
	}
	db, e := catalog.Open(filepath.Join(c.DataDir, "catalogue.sqlite"))
	if e != nil {
		return nil, e
	}
	store, e := cache.New(c.CacheDir, int64(c.CacheGiB)<<30)
	if e != nil {
		db.Close()
		return nil, e
	}
	ctx, cancel := context.WithCancel(ctx)
	a := &App{Measurements: &diagnostics.Recorder{}, Config: m, Catalog: db, Cache: store, Source: adapter, reps: map[string]media.Representation{}, assets: map[string][]library.Entry{}, queued: map[string]bool{}, attempted: map[string]time.Time{}, jobs: make(chan string, 64), ctx: ctx, cancel: cancel, original: c}
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
		a.mount, e = library.MountReadOnly(c.Mountpoint, nil)
		if e != nil {
			a.Close()
			return nil, e
		}
	} else {
		a.mountError = "mount disabled; administration test mode"
	}
	if e = a.Publish(); e != nil {
		a.Close()
		return nil, e
	}
	checkCtx, stop := context.WithTimeout(ctx, 20*time.Second)
	_ = a.Scan(checkCtx)
	stop()
	for i := 0; i < c.Workers; i++ {
		a.wg.Add(1)
		go a.worker()
	}
	a.wg.Add(1)
	go a.schedule()
	return a, nil
}

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

func (a *App) schedule() {
	defer a.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	a.reconcile()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.Config.Reload()
			a.reconcile()
		}
	}
}

func (a *App) reconcile() {
	snap := a.Config.Snapshot()
	c := snap.Config
	a.mu.Lock()
	changed := snap.Revision != a.appliedRevision
	previous := a.applied
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
			_ = a.Queue(ch.ID)
		}
	}
	if changed {
		_ = a.Publish()
	}
	if changed || time.Since(a.lastIntegration) >= time.Minute {
		a.lastIntegration = time.Now()
		ctx, stop := context.WithTimeout(a.ctx, 20*time.Second)
		_ = a.integration(ctx, false)
		stop()
	}
}

func (a *App) worker() {
	defer a.wg.Done()
	for {
		select {
		case <-a.ctx.Done():
			return
		case id := <-a.jobs:
			a.mu.Lock()
			a.attempted[id] = time.Now()
			a.mu.Unlock()
			ctx, cancel := context.WithTimeout(a.ctx, 2*time.Hour)
			job, e := a.Catalog.StartJob(ctx, id)
			if e == nil {
				e = a.Sync(ctx, id)
			}
			cancel()
			finishCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			_ = a.Catalog.FinishJob(finishCtx, job, id, e)
			stop()
			a.mu.Lock()
			delete(a.queued, id)
			a.mu.Unlock()
			a.prune()
		}
	}
}

func (a *App) prune() {
	videos, e := a.Catalog.Videos(a.ctx)
	if e != nil {
		return
	}
	enabled := map[string]bool{}
	for _, ch := range a.Config.Snapshot().Config.Channels {
		enabled[ch.ID] = ch.Enabled
	}
	active, assets := map[string]bool{}, map[string]bool{}
	for _, v := range videos {
		if v.Exposed && enabled[v.ChannelID] {
			active[v.Revision] = true
			assets[v.ID] = true
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.queued) > 0 {
		return
	}
	for id := range a.reps {
		if !active[id] {
			delete(a.reps, id)
		}
	}
	for id := range a.assets {
		if !assets[id] {
			delete(a.assets, id)
		}
	}
}

func (a *App) AddChannel(ctx context.Context, raw string, limit int, expected string) error {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 10000 {
		return errors.New("channel limit must be between 1 and 10000")
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
	snap.Config.Channels = append(snap.Config.Channels, config.Channel{ID: ch.ID, URL: url, Limit: limit, Enabled: true})
	if e = a.Config.Save(snap.Config, expected); e != nil {
		return e
	}
	if e = a.Catalog.EnsureChannel(ctx, ch.ID, ch.Title); e != nil {
		return errors.New("settings saved; catalogue channel initialization failed")
	}
	return a.Queue(ch.ID)
}

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

func (a *App) Sync(ctx context.Context, id string) error {
	defer a.Measurements.Begin("sync")()
	a.mu.RLock()
	toolError := a.toolError
	a.mu.RUnlock()
	if toolError != "" {
		return errors.New(toolError)
	}
	snap, e := a.Source.Enumerate(ctx, id)
	if e != nil {
		return e
	}
	if snap.ID != id {
		return errors.New("channel identity changed")
	}
	conf := a.Config.Snapshot().Config
	limit := 0
	for _, ch := range conf.Channels {
		if ch.ID == id && ch.Enabled {
			limit = ch.Limit
			seen := map[string]bool{}
			for _, e := range snap.Entries {
				seen[e.ID] = true
			}
			for _, explicit := range ch.Unlisted {
				if !seen[explicit] {
					snap.Entries = append(snap.Entries, source.Entry{ID: explicit, Group: -1})
				}
			}
		}
	}
	if limit == 0 {
		return context.Canceled
	}
	// Flat entries retain enumeration order within a tab; dated entries sort first.
	// Metadata is loaded until the exposed window is filled; identity rows for the
	// complete enumeration are committed even when their media is not selected.
	sort.SliceStable(snap.Entries, func(i, j int) bool { return snap.Entries[i].Timestamp > snap.Entries[j].Timestamp })
	prepared := []catalog.Prepared{}
	prior, err := a.Catalog.Videos(ctx)
	if err != nil {
		return err
	}
	previous := map[string]catalog.Video{}
	for _, v := range prior {
		previous[v.ID] = v
	}
	prepared, e = prepareWindow(ctx, a.Source, snap, limit, func(m source.Metadata) (catalog.Prepared, error) {
		p, err := a.Prepare(ctx, m, conf.MaxHeight)
		if err != nil {
			p = catalog.Prepared{Metadata: m, Fallback: "No verified supported representation"}
			if old, ok := previous[m.ID]; ok && old.Revision != "" {
				p.Revision, p.Height, p.Size = old.Revision, old.Height, old.Size
				p.Fallback = "Source verification failed; previous verified revision retained"
			}
		}
		return p, nil
	})
	if e != nil {
		return e
	}
	current := a.Config.Snapshot().Config
	for _, ch := range current.Channels {
		if ch.ID == id && ch.Enabled {
			limit = ch.Limit
			goto commit
		}
	}
	return context.Canceled
commit:
	if e = a.Catalog.CommitSnapshot(ctx, snap, prepared, limit); e != nil {
		return e
	}
	if e = a.Publish(); e != nil {
		return e
	}
	return a.Scan(ctx)
}

func (a *App) Prepare(ctx context.Context, m source.Metadata, maxHeight int) (catalog.Prepared, error) {
	defer a.Measurements.Begin("prepare")()
	if m.Date().IsZero() {
		return catalog.Prepared{}, errors.New("source publication date is unavailable")
	}
	candidates := source.Candidates(m, maxHeight)
	client := source.HTTPClient()
	failure := ""
	attempts := 0
	for _, candidate := range candidates {
		// Bound candidate work without dropping the finite combined fallback.
		if !candidate.Combined && attempts >= 6 {
			continue
		}
		attempts++
		buildCtx, stop := context.WithTimeout(ctx, 5*time.Minute)
		v, e := diagnostics.Measure(a.Measurements, "media.probe", func() (*media.HTTPSource, error) {
			return media.OpenHTTP(buildCtx, client, candidate.Video.URL, m.ID+":"+candidate.Video.ID, a.Cache)
		})
		var rep media.Representation
		indexPath := ""
		if e == nil {
			if candidate.Combined {
				var details media.Details
				details, e = media.InspectCombined(buildCtx, v)
				rep = media.Described{Representation: v, Info: details}
			} else {
				var au *media.HTTPSource
				au, e = diagnostics.Measure(a.Measurements, "media.probe", func() (*media.HTTPSource, error) {
					return media.OpenHTTP(buildCtx, client, candidate.Audio.URL, m.ID+":"+candidate.Audio.ID, a.Cache)
				})
				if e == nil {
					var mux *media.VirtualMP4
					h := sha256.Sum256([]byte(m.ID + ":" + candidate.Video.ID + ":" + candidate.Audio.ID))
					indexPath = filepath.Join(a.original.IndexDir, hex.EncodeToString(h[:])+".json")
					mux, e = diagnostics.Measure(a.Measurements, "media.index.load", func() (*media.VirtualMP4, error) {
						return media.LoadIndex(indexPath, map[string]media.Representation{v.ID(): v, au.ID(): au})
					})
					if e != nil || mux.Details().Height == 0 {
						mux, e = diagnostics.Measure(a.Measurements, "media.index.build", func() (*media.VirtualMP4, error) { return media.Mux(buildCtx, v, au) })
					}
					if e == nil {
						rep = mux
					}
				}
			}
		}
		if e == nil {
			e = diagnostics.Run(a.Measurements, "media.verify", func() error { return media.VerifyRanges(buildCtx, rep) })
		}
		stop()
		if e == nil {
			if described, ok := rep.(media.MediaRepresentation); !ok || described.Details().Height <= 0 || described.Details().Height > maxHeight {
				e = media.ErrUnsupported
			} else {
				candidate.Height = described.Details().Height
			}
		}
		if e != nil {
			if failure == "" {
				failure = fallbackReason(e)
			}
			continue
		}
		if indexed, ok := rep.(*media.VirtualMP4); ok {
			if e = indexed.SaveIndex(filepath.Join(a.original.IndexDir, rep.ID()+".mp4-index.json")); e == nil {
				e = indexed.SaveIndex(indexPath)
			}
			if e != nil {
				if failure == "" {
					failure = fallbackReason(e)
				}
				continue
			}
		}
		described := rep.(media.MediaRepresentation)
		audioFormat := ""
		if candidate.Audio != nil {
			audioFormat = candidate.Audio.ID
		}
		descriptor := struct {
			VideoID, Revision, VideoFormat, AudioFormat string
			Size                                        int64
			Media                                       media.Details
		}{m.ID, rep.ID(), candidate.Video.ID, audioFormat, rep.Size(), described.Details()}
		blob, _ := json.Marshal(descriptor)
		if e = config.AtomicWrite(filepath.Join(a.original.IndexDir, rep.ID()+".description.json"), blob, 0o600); e != nil {
			if failure == "" {
				failure = fallbackReason(e)
			}
			continue
		}
		a.mu.Lock()
		a.reps[rep.ID()] = rep
		a.mu.Unlock()
		assets, _ := diagnostics.Measure(a.Measurements, "assets", func() ([]library.Entry, error) { return a.acquireAssets(ctx, m, candidate), nil })
		a.mu.Lock()
		if len(assets) > 0 {
			a.assets[m.ID] = assets
		}
		a.mu.Unlock()
		return catalog.Prepared{Metadata: m, Revision: rep.ID(), Height: candidate.Height, Fallback: failure, Size: rep.Size()}, nil
	}
	return catalog.Prepared{}, errors.New("no verified supported representation")
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

type unavailable struct {
	id   string
	size int64
}

func (r unavailable) ID() string                                         { return r.id }
func (r unavailable) Size() int64                                        { return r.size }
func (r unavailable) ReadAt(context.Context, []byte, int64) (int, error) { return 0, media.ErrExpired }

func (a *App) Publish() error {
	defer a.Measurements.Begin("publish")()
	a.publishMu.Lock()
	defer a.publishMu.Unlock()
	videos, e := a.Catalog.Videos(a.ctx)
	if e != nil {
		return e
	}
	channels, e := a.Catalog.Channels(a.ctx)
	if e != nil {
		return e
	}
	names := map[string]string{}
	for _, ch := range channels {
		names[ch.ID] = ch.Title
	}
	enabled := map[string]bool{}
	for _, ch := range a.Config.Snapshot().Config.Channels {
		enabled[ch.ID] = ch.Enabled
	}
	episodes := []library.Episode{}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, v := range videos {
		if !v.Exposed || !enabled[v.ChannelID] {
			continue
		}
		rep := a.reps[v.Revision]
		if rep == nil {
			rep = unavailable{id: v.Revision, size: v.Size}
		}
		episodes = append(episodes, library.Episode{ChannelID: v.ChannelID, ChannelTitle: names[v.ChannelID], VideoID: v.ID, Title: v.Title, Description: v.Description, Year: v.Year, Number: v.Number, Media: rep, Modified: v.Updated})
	}
	entries, e := library.Entries(episodes)
	if e != nil {
		return e
	}
	posters := map[string]bool{}
	for _, ep := range episodes {
		base := path.Join("Channel-"+ep.ChannelID, fmt.Sprintf("Season %04d", ep.Year), fmt.Sprintf("Channel-%s - S%04dE%04d", ep.ChannelID, ep.Year, ep.Number))
		assets := a.assets[ep.VideoID]
		if len(assets) == 0 {
			assets = a.loadAssets(ep.VideoID)
		}
		for _, asset := range assets {
			entry := asset
			entry.Path = base + asset.Path
			entry.Modified = ep.Modified
			entries = append(entries, entry)
			if asset.Path == ".jpg" && !posters[ep.ChannelID] {
				poster := asset
				poster.Path = path.Join("Channel-"+ep.ChannelID, "poster.jpg")
				poster.Modified = ep.Modified
				entries = append(entries, poster)
				posters[ep.ChannelID] = true
			}
		}
	}
	if a.mount != nil {
		return a.mount.Update(entries)
	}
	return nil
}

func (a *App) plexClient() (*plex.Client, error) {
	p := a.Config.Snapshot().Config.Plex
	if p.URL == "" {
		return nil, errors.New("Plex connection is not configured")
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

func (a *App) Scan(ctx context.Context) error {
	defer a.Measurements.Begin("plex.scan")()
	return a.integration(ctx, true)
}

func (a *App) integration(ctx context.Context, scan bool) error {
	if a.mount == nil {
		return nil
	}
	if !a.mount.Ready() {
		a.mu.Lock()
		a.mountError = "Library mount is unavailable; scans are disabled"
		a.mu.Unlock()
		return nil
	}
	c, e := a.plexClient()
	if e != nil {
		a.setPlexError(e)
		return nil
	}
	if _, e = c.Check(ctx); e == nil {
		e = c.LibraryHealth(ctx, a.Config.Snapshot().Config.Plex.Section)
	}
	if e == nil && scan {
		e = c.Scan(ctx, a.Config.Snapshot().Config.Plex.Section)
	}
	if e == nil {
		bindings, err := c.Bindings(ctx, a.Config.Snapshot().Config.Plex.Section)
		if err == nil {
			for _, binding := range bindings {
				if id := plex.SourceID(binding.GUID); id != "" {
					_ = a.Catalog.Bind(ctx, id, a.Config.Snapshot().Config.Plex.URL, binding.Key, binding.GUID)
				}
			}
		}
	}
	a.setPlexError(e)
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
	enabled := map[string]bool{}
	for _, ch := range s.Config.Config.Channels {
		enabled[ch.ID] = ch.Enabled
	}
	for i := range s.Videos {
		v := &s.Videos[i]
		if !enabled[v.ChannelID] {
			v.Exposed = false
		}
		if v.Exposed && a.reps[v.Revision] == nil {
			v.Fallback = "Restoring verified representation after restart"
		}
	}
	a.mu.RUnlock()
	s.ConfigError = s.Config.Error
	if cacheError != "" {
		s.ConfigError = cacheError
	}
	s.Ready = s.MountReady && s.SourceError == "" && s.PlexError == "" && !s.RestartRequired
	for _, v := range s.Videos {
		if v.Exposed && v.Fallback == "Restoring verified representation after restart" {
			s.Ready = false
		}
	}
	return s, nil
}

// localAsset is bounded metadata, not an evictable media or transcode file.
type localAsset struct {
	file string
	size int64
}

func (r localAsset) ID() string  { return filepath.Base(r.file) }
func (r localAsset) Size() int64 { return r.size }
func (r localAsset) ReadAt(ctx context.Context, p []byte, o int64) (int, error) {
	if e := ctx.Err(); e != nil {
		return 0, e
	}
	f, e := os.Open(r.file)
	if e != nil {
		return 0, e
	}
	defer f.Close()
	return f.ReadAt(p, o)
}

func (a *App) saveAsset(id, suffix string, b []byte) (library.Entry, error) {
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

func fetchAsset(ctx context.Context, raw string) ([]byte, error) {
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
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, errors.New("asset unavailable")
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 8<<20+1))
	if e != nil || len(b) > 8<<20 {
		return nil, errors.New("asset exceeds metadata size limit")
	}
	return b, nil
}
