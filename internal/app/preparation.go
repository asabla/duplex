package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/source"
)

const finishTimeout = 5 * time.Second

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
