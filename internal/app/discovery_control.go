package app

import (
	"context"
	"errors"
	"time"
)

// Each discovery owns cancellation independently of shared playback resolution.
func (a *App) startDiscovery(ctx context.Context) (context.Context, func(), error) {
	if err := a.waitBackground(ctx); err != nil {
		return ctx, nil, err
	}

	work, cancel := context.WithCancelCause(ctx)
	p := a.playbackConfig()
	a.mu.Lock()
	if len(a.discoveryRuns) >= maxSharedWork {
		a.mu.Unlock()
		cancel(nil)

		return ctx, nil, errors.New("discovery capacity reached")
	}

	if a.discoveryRuns == nil {
		a.discoveryRuns = map[uint64]context.CancelCauseFunc{}
	}

	a.admissionSequence++
	id := a.admissionSequence

	a.discoveryRuns[id] = cancel
	if a.playbackStatusLocked(p, time.Now()).BackgroundPaused {
		cancel(errBackgroundDeferred)
	}

	a.mu.Unlock()

	return work, func() {
		a.mu.Lock()
		delete(a.discoveryRuns, id)
		a.mu.Unlock()
		cancel(nil)
	}, nil
}

func (a *App) syncWhenIdle(ctx context.Context, id string) error {
	for {
		err := a.Sync(ctx, id)
		if !errors.Is(err, errBackgroundDeferred) || ctx.Err() != nil {
			return err
		}
	}
}
