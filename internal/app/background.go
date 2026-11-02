package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/source"
)

var errBackgroundDeferred = errors.New("background work deferred for playback")

type backgroundContextKey struct{}

func backgroundContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundContextKey{}, true)
}

func (a *App) waitBackground(ctx context.Context) error {
	defer a.Measurements.Begin("background.wait")()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		if !a.PlaybackStatus().BackgroundPaused {
			return nil
		}

		a.mu.Lock()
		if a.playbackWake == nil {
			a.playbackWake = make(chan struct{})
		}

		wake := a.playbackWake
		a.mu.Unlock()

		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
		case <-wake:
		case <-timer.C:
		}

		timer.Stop()
	}
}

func (a *App) backgroundSlot(kind string) chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()

	var slot *chan struct{}

	switch kind {
	case "metadata":
		slot = &a.backgroundMetadata
	case "preparation":
		slot = &a.backgroundPreparation
	default:
		slot = &a.backgroundHeavy
	}

	if *slot == nil {
		*slot = make(chan struct{}, 1)
	}

	return *slot
}

func (a *App) acquireBackground(ctx context.Context, kind string) (func(), error) {
	slot := a.backgroundSlot(kind)

	for {
		if err := a.waitBackground(ctx); err != nil {
			return nil, err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case slot <- struct{}{}:
		}

		if !a.PlaybackStatus().BackgroundPaused {
			return func() { <-slot }, nil
		}

		<-slot
	}
}

// Claim only stream renewals while paused. Ordinary and manually requested
// preparations share one admission slot, leaving foreground binding independent.
func (a *App) nextPreparation(ctx context.Context) (catalog.PreparationTask, func(), error) {
	if a.backgroundPaused() {
		task, err := a.Catalog.ClaimStreamRenewal(ctx)
		return task, func() {}, err
	}

	slot := a.backgroundSlot("preparation")
	select {
	case slot <- struct{}{}:
	default:
		return catalog.PreparationTask{}, nil, sql.ErrNoRows
	}

	release := func() { <-slot }

	task, err := a.Catalog.ClaimPreparationTask(ctx)
	if err != nil || task.Priority == streamRenewalPriority {
		release()
		release = func() {}
	}

	return task, release, err
}

func (a *App) discoveryMetadata(ctx context.Context, id string) (source.Metadata, error) {
	if m, ok, err := a.cachedMetadata(id); ok && err == nil {
		return m, nil
	}

	release, err := a.acquireBackground(ctx, "metadata")
	if err != nil {
		return source.Metadata{}, err
	}
	defer release()

	return a.Source.Metadata(source.WithCause(ctx, source.CauseDiscovery), id)
}

func (a *App) automaticUpdatesAllowed() bool {
	p := a.playbackConfig()
	return p.URL != "" && p.UpdatesEnabled() && !a.PlaybackStatus().BackgroundPaused
}

func (a *App) admitPlexUpdate(ctx context.Context) (func(), error) {
	if !a.automaticUpdatesAllowed() {
		return nil, errBackgroundDeferred
	}

	release, err := a.acquireBackground(ctx, "heavy")
	if err != nil {
		return nil, err
	}

	if !a.automaticUpdatesAllowed() {
		release()
		return nil, errBackgroundDeferred
	}

	return release, nil
}

func (a *App) backgroundChannel(ctx context.Context, raw string) (source.Channel, error) {
	release, err := a.acquireBackground(ctx, "metadata")
	if err != nil {
		return source.Channel{}, err
	}
	defer release()

	return a.Source.ResolveChannel(ctx, raw)
}
