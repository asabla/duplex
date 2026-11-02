package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"duplex/internal/media"
	"duplex/internal/source"
)

const (
	metadataEntries   = 64
	metadataBudget    = 8 << 20
	resolutionBackoff = time.Second
)

type metadataLease struct {
	value         source.Metadata
	expires, used time.Time
	bytes         int
	failure       error
}

func (a *App) cachedMetadata(id string) (source.Metadata, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	lease, ok := a.metadata[id]
	if !ok || !time.Now().Before(lease.expires) {
		return source.Metadata{}, false, nil
	}

	lease.used = time.Now()
	a.metadata[id] = lease
	a.Measurements.Count("resolution.cache_hit", 1)

	return lease.value, true, lease.failure
}

func (a *App) remember(m source.Metadata) {
	if len(m.Formats) > 0 {
		a.rememberResolution(m.ID, m, nil)
	}
}

func (a *App) rememberResolution(id string, m source.Metadata, failure error) {
	if errors.Is(failure, context.Canceled) {
		return
	}

	if failure != nil {
		m = source.Metadata{}
		failure = resolutionFailure(failure)
	}

	if len(m.Description)+len(m.Formats)*256+len(id) > metadataBudget {
		return
	}

	encoded, err := json.Marshal(m)
	if err != nil {
		return
	}

	cost := len(encoded)*2 + len(m.Formats)*256 + len(id) + 4096
	if cost > metadataBudget {
		return
	}

	now := time.Now()

	expires := leaseExpiry(m)
	if expires.After(now.Add(5 * time.Minute)) {
		expires = now.Add(5 * time.Minute)
	}

	if len(m.Formats) == 0 {
		expires = now.Add(time.Minute)
	}

	if failure != nil {
		expires = now.Add(resolutionBackoff)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.metadata == nil {
		a.metadata = map[string]metadataLease{}
	}

	for key, lease := range a.metadata {
		if !now.Before(lease.expires) {
			delete(a.metadata, key)
		}
	}

	a.metadata[id] = metadataLease{value: m, expires: expires, used: now, bytes: cost, failure: failure}
	a.trimMetadataLocked()
}

func resolutionFailure(err error) error {
	for _, kind := range []error{source.ErrUnavailable, context.DeadlineExceeded, media.ErrExpired, media.ErrChanged, media.ErrUnsupported} {
		if errors.Is(err, kind) {
			return kind
		}
	}

	return errors.New("source resolution failed; retry is delayed")
}

func (a *App) forgetMetadata(id string) {
	a.mu.Lock()
	delete(a.metadata, id)
	a.mu.Unlock()
}

func (a *App) trimMetadataLocked() {
	for {
		cost, oldest := 0, ""
		for id, lease := range a.metadata {
			cost += lease.bytes
			if oldest == "" || lease.used.Before(a.metadata[oldest].used) {
				oldest = id
			}
		}

		if len(a.metadata) <= metadataEntries && cost <= metadataBudget {
			return
		}

		delete(a.metadata, oldest)
		a.Measurements.Count("resolution.eviction", 1)
	}
}

func (a *App) playbackMetadata(ctx context.Context, id string, force bool) (source.Metadata, error) {
	defer a.Measurements.Begin("playback.metadata")()

	if err := ctx.Err(); err != nil {
		return source.Metadata{}, err
	}

	if !force {
		if m, ok, err := a.cachedMetadata(id); ok {
			return m, err
		}
	}

	a.Measurements.Count("resolution.cache_miss", 1)

	if ctx.Value(backgroundContextKey{}) == true {
		release, err := a.acquireBackground(ctx, "metadata")
		if err != nil {
			return source.Metadata{}, err
		}
		defer release()
	}

	// Admission can wait behind other work. Recheck before joining a fresh job.
	if !force {
		if m, ok, err := a.cachedMetadata(id); ok {
			return m, err
		}
	}

	// Admission occurs before sharing. A foreground waiter can join admitted
	// background work without inheriting a wait for Plex to become idle. Every
	// shared callback resolves afresh, including when a forced caller joins it.
	m, shared, err := a.resolutionWork.do(ctx, a.ctx, id, func(work context.Context) (source.Metadata, error) {
		return a.resolveMetadata(work, id)
	})
	if shared {
		a.Measurements.Count("resolution.coalesced_waiters", 1)
	}

	return m, err
}

func (a *App) acquireResolution(ctx context.Context) (func(), error) {
	defer a.Measurements.Begin("resolution.wait")()

	a.mu.Lock()
	if a.resolutionSlots == nil {
		a.resolutionSlots = make(chan struct{}, 2)
	}

	slots := a.resolutionSlots
	a.mu.Unlock()

	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *App) resolveMetadata(work context.Context, id string) (source.Metadata, error) {
	release, err := a.acquireResolution(work)
	if err != nil {
		return source.Metadata{}, err
	}
	defer release()

	a.Measurements.Count("resolution.request", 1)
	a.Measurements.Count("resolution."+source.CauseName(work), 1)

	m, err := a.Source.Resolve(work, id)
	if work.Err() != nil {
		return source.Metadata{}, work.Err()
	}

	if err == nil && (m.ID != id || m.ChannelID == "") {
		err = errors.New("source video identity changed")
	}

	a.rememberResolution(id, m, err)

	return m, err
}
