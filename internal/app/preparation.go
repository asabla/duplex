package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/source"
)

const finishTimeout = 5 * time.Second

func (a *App) metadataFresh(v catalog.Video, entry source.Entry) bool {
	return time.Since(time.Unix(v.MetadataAt, 0)) < 6*time.Hour && (entry.Title == "" || entry.Title == v.Title)
}

type metadataLease struct {
	value   source.Metadata
	expires time.Time
}

func visibility(c config.Config) catalog.Visibility {
	var v catalog.Visibility
	for _, ch := range c.Channels {
		if !ch.Enabled {
			continue
		}
		v.Channels = append(v.Channels, ch.ID)
		if ch.Playlists == nil || !ch.Playlists.Enabled {
			continue
		}
		v.PlaylistChannels = append(v.PlaylistChannels, ch.ID)
		for id, p := range ch.Playlists.Overrides {
			if p.Mode == "skip" {
				v.SkippedPlaylists = append(v.SkippedPlaylists, id)
			}
		}
	}
	return v
}

func (a *App) remember(m source.Metadata) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.metadata == nil {
		a.metadata = map[string]metadataLease{}
	}
	if len(a.metadata) >= 8 {
		for id := range a.metadata {
			delete(a.metadata, id)
			break
		}
	}
	a.metadata[m.ID] = metadataLease{m, leaseExpiry(m)}
}

func leaseExpiry(m source.Metadata) time.Time {
	until := time.Now().Add(5 * time.Minute)
	found := false
	for _, f := range m.Formats {
		u, err := url.Parse(f.URL)
		if err != nil {
			continue
		}
		expiry, err := strconv.ParseInt(u.Query().Get("expire"), 10, 64)
		if err != nil {
			continue
		}
		t := time.Unix(expiry, 0).Add(-time.Minute)
		if !found || t.Before(until) {
			until = t
			found = true
		}
	}
	return until
}

func (a *App) playbackMetadata(ctx context.Context, id string) (source.Metadata, error) {
	a.mu.RLock()
	lease, ok := a.metadata[id]
	a.mu.RUnlock()
	if ok && time.Now().Before(lease.expires) {
		return lease.value, nil
	}
	m, err := a.Source.Resolve(ctx, id)
	if err == nil {
		a.remember(m)
	}
	return m, err
}

func (a *App) usable(ctx context.Context, id string) (bool, error) {
	revision, err := a.Catalog.ActiveRevision(ctx, id)
	if err != nil {
		return false, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.reps[revision] != nil && time.Now().Before(a.leases[revision]), nil
}

func (a *App) queuePreparation(ctx context.Context, id string, priority int) error {
	usable, err := a.usable(ctx, id)
	if err != nil || usable {
		return err
	}
	if err = a.Catalog.QueuePreparation(ctx, id, priority); err != nil {
		return err
	}
	a.wakePreparation()
	return nil
}

func (a *App) wakePreparation() {
	select {
	case a.prepareWake <- struct{}{}:
	default:
	}
}

// PrepareNow raises an already selected recording above background work.
func (a *App) PrepareNow(ctx context.Context, id string) error {
	selected, err := a.Catalog.IsSelected(ctx, id, visibility(a.Config.Snapshot().Config))
	if err != nil {
		return err
	}
	if !selected {
		return errors.New("recording is outside the selected library")
	}
	return a.queuePreparation(ctx, id, 1000)
}

func (a *App) preparationWorker() {
	defer a.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := a.ctx.Err(); err != nil {
			return
		}
		id, err := a.Catalog.ClaimPreparation(a.ctx)
		if errors.Is(err, sql.ErrNoRows) {
			select {
			case <-a.ctx.Done():
				return
			case <-a.prepareWake:
			case <-ticker.C:
			}
			continue
		}
		if err != nil {
			a.operationFailure("preparation queue", err)
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
			}
			continue
		}
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Minute)
		err = a.prepareRecording(ctx, id)
		cancel()
		if err != nil {
			a.operationFailure("recording preparation", err)
			cleanup, stop := context.WithTimeout(context.WithoutCancel(a.ctx), 5*time.Second)
			if stateErr := a.Catalog.SetPreparation(cleanup, id, "failed", fallbackReason(err)); stateErr != nil {
				a.operationFailure("preparation result", stateErr)
			}
			stop()
		}
	}
}

func (a *App) prepareRecording(ctx context.Context, id string) error {
 a.mu.RLock();toolError:=a.toolError;a.mu.RUnlock()
 if toolError!=""{return errors.New(toolError)}
	selected, err := a.Catalog.IsSelected(ctx, id, visibility(a.Config.Snapshot().Config))
	if err != nil {
		return err
	}
	if !selected {
		return a.Catalog.SetPreparation(ctx, id, "skipped", "")
	}
	usable, err := a.usable(ctx, id)
	if err != nil {
		return err
	}
	if usable {
		return a.Catalog.SetPreparation(ctx, id, "ready", "")
	}
	if err = a.Catalog.SetPreparation(ctx, id, "resolving", ""); err != nil {
		return err
	}
	m, err := a.playbackMetadata(ctx, id)
	if err != nil {
		return err
	}
	if m.ID != id || m.ChannelID=="" {
		return errors.New("source video identity changed")
	}
	if err = a.Catalog.SaveMetadata(ctx, m); err != nil {
		return err
	}
	if !recording(m) {
		return a.Catalog.SetPreparation(ctx, id, "waiting", "Waiting for a completed recording")
	}
	if err = a.Catalog.SetPreparation(ctx, id, "preparing", ""); err != nil {
		return err
	}
	p, err := a.Prepare(ctx, m, a.Config.Snapshot().Config.MaxHeight)
	if err != nil {
		return err
	}
	if err = a.Catalog.SavePrepared(ctx, p); err != nil {
		return err
	}
	if err = a.Catalog.QueueAsset(ctx, id); err != nil {
		return err
	}
	if err = a.Publish(); err != nil {
		return err
	}
	a.requestScan()
	return nil
}

func recording(m source.Metadata) bool { return m.State() == "recorded" || m.State() == "replay-ready" }

func (a *App) operationFailure(operation string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	// Do not log error strings: process and transport errors can contain locators.
	slog.Error("duplex operation failed", "operation", operation)
	a.Measurements.Add("failure."+operation, 0)
}

func (a *App) requestScan() {
	a.mu.Lock()
	a.scanPending = true
	a.mu.Unlock()
	select {
	case a.scanWake <- struct{}{}:
	default:
	}
}

func (a *App) scanWorker() {
	defer a.wg.Done()
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	var last time.Time
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.scanWake:
		case <-timer.C:
		}
		a.mu.Lock()
		scan := a.scanPending && time.Since(last) >= 10*time.Second
		if scan {
			a.scanPending = false
		}
		a.mu.Unlock()
		if !scan {
			continue
		}
		ctx, stop := context.WithTimeout(a.ctx, 20*time.Second)
		err := a.Scan(ctx)
		stop()
		a.operationFailure("Plex scan", err)
		last = time.Now()
	}
}

func (a *App) assetWorker() {
	defer a.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
		id, err := a.Catalog.NextAsset(a.ctx)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			a.operationFailure("asset queue", err)
			continue
		}
		ctx, stop := context.WithTimeout(a.ctx, time.Minute)
		m, err := a.playbackMetadata(ctx, id)
		if err == nil {
			candidates := source.Candidates(m, a.Config.Snapshot().Config.MaxHeight)
			if len(candidates) == 0 {
				err = errors.New("no supported asset language")
			} else {
				assets := a.acquireAssets(ctx, m, candidates[0])
				if len(assets) > 0 {
					a.mu.Lock()
					a.assets[id] = assets
					a.mu.Unlock()
					err = a.Publish()
				}
			}
		}
		stop()
		a.operationFailure("optional assets", err)
		if finishErr := a.Catalog.FinishAsset(a.ctx, id, err); finishErr != nil {
			a.operationFailure("asset result", finishErr)
		}
	}
}
