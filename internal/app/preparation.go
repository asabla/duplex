package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/diagnostics"
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
	if len(m.Formats) == 0 {
		return
	}
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

func (a *App) playbackMetadata(ctx context.Context, id string, force bool) (source.Metadata, error) {
	a.mu.RLock()
	lease, ok := a.metadata[id]
	a.mu.RUnlock()
	if !force && ok && time.Now().Before(lease.expires) {
		return lease.value, nil
	}
	m, err := a.Source.Resolve(ctx, id)
	if err == nil {
		a.remember(m)
	}
	return m, err
}

func (a *App) usable(ctx context.Context, id string) (bool, error) {
	revision, state, err := a.Catalog.RepresentationState(ctx, id)
	if err != nil {
		return false, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return (state == "recorded" || state == "replay-ready") && a.reps[revision] != nil && time.Now().Before(a.leases[revision]), nil
}

func (a *App) queuePreparation(ctx context.Context, id string, priority int) error {
	ready, err := a.Catalog.PreparedForHeight(ctx, id, a.Config.Snapshot().Config.MaxHeight)
	if err != nil || (ready && priority < 1000) {
		return err
	}
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
	usable, err := a.usable(ctx, id)
	if err != nil || usable {
		return err
	}
	if err = a.Catalog.QueuePreparation(ctx, id, 1000); err != nil {
		return err
	}
	requested, err := a.Catalog.RequestedPreparations(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.interruptBackgroundLocked(max(0, 2-a.foreground-requested))
	a.mu.Unlock()
	a.wakePreparation()
	return nil
}

func (a *App) preparationWorker(ctx context.Context) {
	defer a.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if a.backgroundPaused() {
			if !a.waitPreparation(ctx, ticker.C) {
				return
			}
			continue
		}
		task, err := a.Catalog.ClaimPreparationTask(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			if !a.waitPreparation(ctx, ticker.C) {
				return
			}
			continue
		}
		if err != nil {
			a.operationFailure("preparation queue", err)
			if !a.waitPreparation(ctx, ticker.C) {
				return
			}
			continue
		}
		a.runPreparation(ctx, task)
	}
}

func (a *App) waitPreparation(ctx context.Context, tick <-chan time.Time) bool {
	select {
	case <-ctx.Done():
		return false
	case <-a.prepareWake:
		return true
	case <-tick:
		return true
	}
}

func (a *App) runPreparation(ctx context.Context, task catalog.PreparationTask) {
	if task.QueuedAt > 0 {
		a.Measurements.Add("preparation.queue", max(time.Duration(0), time.Since(time.Unix(task.QueuedAt, 0))))
	}
	if task.Priority >= 1000 {
		finished := a.beginForeground()
		defer finished()
	}
	taskContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	workContext, timeout := context.WithTimeout(taskContext, 15*time.Minute)
	defer timeout()
	started := a.startPreparation(task, cancel)
	var err error
	if started {
		err = a.prepareRecording(workContext, task.VideoID, task.Priority == renewalPriority)
		a.endPreparation(task.VideoID)
	}
	if ctx.Err() != nil {
		return
	}
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer stop()
	if !started || errors.Is(context.Cause(taskContext), errPlaybackPriority) {
		a.operationFailure("resume background preparation", a.Catalog.SetPreparation(cleanup, task.VideoID, "queued", ""))
		a.wakePreparation()
		return
	}
	if err != nil {
		a.operationFailure("recording preparation", err)
		a.operationFailure("preparation result", a.Catalog.SetPreparation(cleanup, task.VideoID, "failed", preparationReason(err)))
	}
}

func (a *App) resolveRecording(ctx context.Context, id string, force bool) (source.Metadata, bool, error) {
	a.mu.RLock()
	toolError := a.toolError
	a.mu.RUnlock()
	if toolError != "" {
		return source.Metadata{}, true, errors.New(toolError)
	}
	selected, err := a.Catalog.IsSelected(ctx, id, visibility(a.Config.Snapshot().Config))
	if err != nil {
		return source.Metadata{}, true, err
	}
	if !selected {
		return source.Metadata{}, true, a.Catalog.SetPreparation(ctx, id, "skipped", "")
	}
	usable, err := a.usable(ctx, id)
	if err != nil {
		return source.Metadata{}, true, err
	}
	if usable && !force {
		return source.Metadata{}, true, a.Catalog.SetPreparation(ctx, id, "ready", "")
	}
	if err = a.Catalog.SetPreparation(ctx, id, "resolving", ""); err != nil {
		return source.Metadata{}, true, err
	}
	m, err := a.playbackMetadata(ctx, id, force)
	if errors.Is(err, source.ErrUnavailable) {
		return source.Metadata{}, true, a.unavailableRecording(ctx, id)
	}
	if err != nil {
		return source.Metadata{}, true, err
	}
	if m.ID != id || m.ChannelID == "" {
		return source.Metadata{}, true, errors.New("source video identity changed")
	}
	if err = a.Catalog.SaveMetadata(ctx, m); err != nil {
		return source.Metadata{}, true, err
	}
	if !recording(m) {
		return source.Metadata{}, true, a.Catalog.SetPreparation(ctx, id, "waiting", "Waiting for a completed recording")
	}
	return m, false, nil
}

func (a *App) prepareRecording(ctx context.Context, id string, force bool) error {
	m, done, err := a.resolveRecording(ctx, id, force)
	if done || err != nil {
		return err
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
	if err = errors.Join(err, a.Publish()); err != nil {
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

func (a *App) scanWorker(ctx context.Context) {
	defer a.wg.Done()
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	state := scanSchedule{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.scanWake:
		case <-timer.C:
		}
		mode, run := a.nextScan(time.Now(), &state)
		if !run {
			continue
		}
		work, stop := context.WithTimeout(ctx, 20*time.Second)
		operation := "plex.reconcile"
		if mode == integrationScan {
			operation = "plex.scan"
		}
		err := diagnostics.Run(a.Measurements, operation, func() error { return a.integration(work, mode) })
		stop()
		a.operationFailure("Plex scan", err)
		state.finished(mode, err)
	}
}

type scanSchedule struct {
	lastScan time.Time
	retryAt  time.Time
	retries  int
}

func (a *App) nextScan(now time.Time, state *scanSchedule) (integrationMode, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.scanPending && now.Sub(state.lastScan) >= 10*time.Second {
		a.scanPending = false
		state.retries = 0
		return integrationScan, true
	}
	if state.retries > 0 && !now.Before(state.retryAt) {
		state.retries--
		return integrationBindings, true
	}
	return integrationHealth, false
}

func (s *scanSchedule) finished(mode integrationMode, failure error) {
	if mode == integrationScan {
		s.lastScan = time.Now()
		if failure != nil {
			s.retries = 3
		}
	}
	if failure == nil {
		s.retries = 0
	}
	s.retryAt = time.Now().Add(30 * time.Second)
}

func (a *App) assetWorker(ctx context.Context) {
	defer a.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		id, err := a.Catalog.NextAsset(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			a.operationFailure("asset queue", err)
			continue
		}
		workCtx, stop := context.WithTimeout(ctx, time.Minute)
		m, err := a.playbackMetadata(workCtx, id, false)
		if err == nil {
			candidates := source.Candidates(m, a.Config.Snapshot().Config.MaxHeight)
			if len(candidates) == 0 {
				err = errors.New("no supported asset language")
			} else {
				assets, assetErr := a.acquireAssets(workCtx, m, candidates[0])
				err = assetErr
				if len(assets) > 0 {
					err = errors.Join(err, a.Publish())
					a.requestScan()
				}
			}
		}
		stop()
		a.operationFailure("optional assets", err)
		if finishErr := a.Catalog.FinishAsset(ctx, id, err); finishErr != nil {
			a.operationFailure("asset result", finishErr)
		}
	}
}

func (a *App) unavailableRecording(ctx context.Context, id string) error {
	if err := a.Catalog.MarkUnavailable(ctx, id); err != nil {
		return err
	}
	if err := a.Catalog.SetPreparation(ctx, id, "waiting", "Source is unavailable without credentials"); err != nil {
		return err
	}
	return a.Publish()
}

func preparationReason(err error) string {
	return strings.Replace(fallbackReason(err), "A higher-quality ", "The recording ", 1)
}
