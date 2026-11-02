package app

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"strconv"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/media"
	"duplex/internal/source"
)

// representationState keeps immutable bindings and expiring locators together.
// App.mu protects its maps; an open handle retains its representation after eviction.
type representationState struct {
	layouts         map[string]layoutLease
	layoutBytes     int64
	layoutSlots     chan struct{}
	runtimeVideos   map[string]string
	renewAfter      map[string]time.Time
	runtimeUsed     map[string]time.Time
	streamUsed      map[string]time.Time
	bindingWork     sharedWork[media.Representation]
	resolutionWork  sharedWork[source.Metadata]
	resolutionSlots chan struct{}
	metadata        map[string]metadataLease
	leases          map[string]time.Time
	reps            map[string]media.Representation
}

const (
	runtimeRepresentations = 128
	runtimeIndexBytes      = int64(64 << 20)
)

func (a *App) acquirePreparation(ctx context.Context) (func(), error) {
	a.mu.Lock()
	if a.prepareSlots == nil {
		a.prepareSlots = make(chan struct{}, 2)
	}

	slots := a.prepareSlots
	a.mu.Unlock()

	finished := a.Measurements.Begin("preparation.wait")
	defer finished()

	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *App) cacheRepresentation(rep media.Representation, expires time.Time, videoID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.reps == nil {
		a.reps = map[string]media.Representation{}
	}

	if a.leases == nil {
		a.leases = map[string]time.Time{}
	}

	if a.runtimeUsed == nil {
		a.runtimeUsed = map[string]time.Time{}
	}

	if a.runtimeVideos == nil {
		a.runtimeVideos = map[string]string{}
	}

	if a.renewAfter == nil {
		a.renewAfter = map[string]time.Time{}
	}

	a.runtimeVideos[rep.ID()] = videoID
	a.reps[rep.ID()] = rep
	a.leases[rep.ID()] = expires

	a.runtimeUsed[rep.ID()] = time.Now()
	for len(a.reps) > 1 && (len(a.reps) > runtimeRepresentations || a.runtimeIndexCost() > runtimeIndexBytes) {
		oldest := ""
		for id := range a.reps {
			if oldest == "" || a.runtimeUsed[id].Before(a.runtimeUsed[oldest]) {
				oldest = id
			}
		}

		delete(a.reps, oldest)
		delete(a.leases, oldest)
		delete(a.runtimeUsed, oldest)
		delete(a.streamUsed, oldest)
		delete(a.runtimeVideos, oldest)
		delete(a.renewAfter, oldest)
	}

	a.trimLayoutsLocked()
}

func (a *App) cachedRepresentation(revision string) media.Representation {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !time.Now().Before(a.leases[revision]) {
		return nil
	}

	rep := a.reps[revision]
	if rep != nil {
		if a.runtimeUsed == nil {
			a.runtimeUsed = map[string]time.Time{}
		}

		a.runtimeUsed[revision] = time.Now()
	}

	return rep
}

func (a *App) recordStreamRead(revision string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.reps[revision] == nil {
		return
	}

	a.recordStreamReadLocked(revision)
}

func (a *App) recordStreamReadLocked(revision string) {
	now := time.Now()
	if now.Sub(a.streamUsed[revision]) >= time.Minute {
		// An inactive stream can have a deferred ordinary renewal. Reconsider
		// it once when reads start, without removing active-stream retry bounds.
		delete(a.renewAfter, revision)
	}

	if a.streamUsed == nil {
		a.streamUsed = map[string]time.Time{}
	}

	a.streamUsed[revision] = now
}

func (a *App) bindEpisode(ctx context.Context, episode catalog.Episode) (media.Representation, error) {
	if rep := a.cachedRepresentation(episode.Revision); rep != nil {
		return rep, nil
	}

	rep, shared, err := a.bindingWork.do(ctx, a.ctx, episode.Revision, func(work context.Context) (media.Representation, error) {
		rep, failure := a.bindUncachedEpisode(work, episode)
		if errors.Is(failure, media.ErrExpired) {
			a.forgetMetadata(episode.VideoID)
		}

		if failure != nil {
			failure = errors.Join(failure, a.recordBindingFailure(work, episode, failure))
		}

		return rep, failure
	})
	if shared {
		a.Measurements.Count("binding.coalesced_waiters", 1)
	}

	return rep, err
}

func (a *App) bindUncachedEpisode(ctx context.Context, episode catalog.Episode) (media.Representation, error) {
	ctx = source.WithCause(ctx, source.CauseFilesystem)

	defer a.Measurements.Begin("playback.binding")()

	if rep := a.cachedRepresentation(episode.Revision); rep != nil {
		return rep, nil
	}

	finished := a.beginForeground()
	defer finished()

	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	selected, err := a.Catalog.IsSelected(ctx, episode.VideoID, visibility(a.Config.Snapshot().Config))
	if err != nil {
		return nil, err
	}

	if !selected {
		return nil, fs.ErrNotExist
	}

	m, err := a.playbackMetadata(ctx, episode.VideoID, false)
	if err != nil {
		return nil, err
	}

	if m.ID != episode.VideoID {
		return nil, media.ErrChanged
	}

	if err = a.Catalog.SaveMetadata(ctx, m); err != nil {
		return nil, err
	}

	if !recording(m) {
		return nil, media.ErrUnsupported
	}

	prepared, rep, err := a.prepareWithRepresentation(ctx, m, a.Config.Snapshot().Config.MaxHeight)
	if err != nil {
		return nil, err
	}

	if err = a.Catalog.SavePrepared(ctx, prepared); err != nil {
		return nil, err
	}

	if prepared.Revision != episode.Revision || prepared.Size != episode.Size {
		a.requestScan()
		return nil, media.ErrChanged
	}

	if err = errors.Join(a.Publish(), a.Catalog.QueueAsset(ctx, episode.VideoID)); err != nil {
		return nil, err
	}

	a.wakeAssets()

	return rep, nil
}

// runtimeIndexCost estimates resident index storage conservatively. One oversized
// index can remain cached; its on-disk parser limit bounds that case separately.
func (a *App) runtimeIndexCost() int64 {
	var total int64

	for _, rep := range a.reps {
		if index, ok := rep.(*media.VirtualMP4); ok {
			total += int64(cap(index.Header)) + int64(cap(index.Spans))*160 + 32<<10
		} else {
			total += 16 << 10
		}
	}

	return total
}

func (a *App) recordBindingFailure(ctx context.Context, episode catalog.Episode, failure error) error {
	if ctx.Err() != nil {
		return nil
	}

	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()

	if errors.Is(failure, source.ErrUnavailable) {
		if err := a.Catalog.MarkUnavailable(cleanup, episode.VideoID); err != nil {
			return err
		}
	}

	revision, state, err := a.Catalog.RepresentationState(cleanup, episode.VideoID)
	if err != nil {
		return err
	}

	// A changed source may already have published a new verified revision. The
	// failed request for an old revision must not mark that new file as failed.
	if revision != episode.Revision {
		return nil
	}

	a.operationFailure("playback source binding", failure)

	if state != "recorded" && state != "replay-ready" {
		return a.Catalog.SetPreparation(cleanup, episode.VideoID, "waiting", "Waiting for an available completed recording")
	}

	return a.Catalog.SetPreparation(cleanup, episode.VideoID, "failed", preparationReason(failure))
}

func (a *App) metadataFresh(v catalog.Video, entry source.Entry) bool {
	return time.Since(time.Unix(v.MetadataAt, 0)) < 6*time.Hour && (entry.Title == "" || entry.Title == v.Title)
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

func (a *App) usable(ctx context.Context, id string) (bool, error) {
	revision, state, err := a.Catalog.RepresentationState(ctx, id)
	if err != nil {
		return false, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	return (state == "recorded" || state == "replay-ready") && a.reps[revision] != nil && time.Now().Before(a.leases[revision]), nil
}
