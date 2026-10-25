package app

import (
	"context"
	"errors"
	"reflect"
	"time"

	"duplex/internal/config"
)

// schedulerState retains queue deduplication and applied configuration between ticks.
// App.mu protects shared fields; lastIntegration belongs only to the scheduler.
type schedulerState struct {
	queued          map[string]bool
	attempted       map[string]time.Time
	jobs            chan string
	appliedRevision string
	lastIntegration time.Time
	applied         config.Config
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
			a.renewCachedSources(ctx)
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
		a.operationFailure("integration health", a.integration(ctx, integrationHealth))
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
			before := a.Config.Snapshot().Config
			a.mu.Lock()
			a.attempted[id] = time.Now()
			a.mu.Unlock()
			jobCtx, cancel := context.WithCancel(ctx)
			err := a.Sync(jobCtx, id)
			cancel()
			a.operationFailure("channel discovery", err)
			a.mu.Lock()
			delete(a.queued, id)
			a.attempted[id] = time.Now()
			a.mu.Unlock()
			if ctx.Err() == nil && a.discoverySettingsChanged(before, id) {
				a.operationFailure("updated discovery settings", a.Queue(id))
			}
		}
	}
}

func (a *App) discoverySettingsChanged(before config.Config, id string) bool {
	current, err := a.followed(id)
	if err != nil {
		return false
	}
	for _, previous := range before.Channels {
		if previous.ID == id {
			return !reflect.DeepEqual(previous, current) || before.MaxHeight != a.Config.Snapshot().Config.MaxHeight
		}
	}
	return true
}
