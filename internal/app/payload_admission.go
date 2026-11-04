package app

import (
	"context"
	"time"

	"duplex/internal/config"
	"duplex/internal/media"
	"duplex/internal/plex"
)

type (
	payloadContextKey struct{}
	payloadRun        struct {
		key    string
		cancel context.CancelCauseFunc
	}
)

func (a *App) payloadAllowedLocked(p config.Plex, key string, now time.Time) bool {
	status := a.playbackStatusLocked(p, now)
	return status.State != "active" || status.Sessions == 0 || a.protectedMedia[key]
}

// NFS has no scanner identity. While Plex has viewers, only their reported file
// slots can initiate uncached payload work. Verified local bytes remain readable.
func (a *App) startPayload(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}

	key, ok := ctx.Value(payloadContextKey{}).(string)
	if !ok {
		key = ""
	}

	p := a.playbackConfig()
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.payloadAllowedLocked(p, key, time.Now()) || len(a.payloadRuns) >= maxSharedWork {
		return ctx, nil, media.ErrDeferred
	}

	work, cancel := context.WithCancelCause(ctx)

	if a.payloadRuns == nil {
		a.payloadRuns = map[uint64]payloadRun{}
	}

	a.admissionSequence++
	id := a.admissionSequence
	a.payloadRuns[id] = payloadRun{key: key, cancel: cancel}

	return work, func() {
		a.mu.Lock()
		delete(a.payloadRuns, id)
		a.mu.Unlock()
		cancel(nil)
	}, nil
}

func payloadContext(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, payloadContextKey{}, plex.MediaKey(name))
}
