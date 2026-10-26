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
	runtimeVideos map[string]string
	renewAfter    map[string]time.Time
	runtimeUsed   map[string]time.Time
	bindings      map[string]*representationBinding
	metadata      map[string]metadataLease
	leases        map[string]time.Time
	reps          map[string]media.Representation
}

const (
	runtimeRepresentations = 128
	runtimeIndexBytes      = int64(64 << 20)
)

type representationBinding struct {
	done chan struct{}
	rep  media.Representation
	err  error
}

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
		delete(a.runtimeVideos, oldest)
		delete(a.renewAfter, oldest)
	}
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

func (a *App) bindEpisode(ctx context.Context, episode catalog.Episode) (media.Representation, error) {
	if rep := a.cachedRepresentation(episode.Revision); rep != nil {
		return rep, nil
	}

	a.mu.Lock()
	if a.bindings == nil {
		a.bindings = map[string]*representationBinding{}
	}

	if call := a.bindings[episode.Revision]; call != nil {
		a.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-call.done:
			return call.rep, call.err
		}
	}

	call := &representationBinding{done: make(chan struct{})}
	a.bindings[episode.Revision] = call
	a.mu.Unlock()

	call.rep, call.err = a.bindUncachedEpisode(ctx, episode)
	if call.err != nil {
		call.err = errors.Join(call.err, a.recordBindingFailure(ctx, episode, call.err))
	}

	a.mu.Lock()
	delete(a.bindings, episode.Revision)
	close(call.done)
	a.mu.Unlock()

	return call.rep, call.err
}

func (a *App) bindUncachedEpisode(ctx context.Context, episode catalog.Episode) (media.Representation, error) {
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

type metadataLease struct {
	value   source.Metadata
	expires time.Time
}

func (a *App) metadataFresh(v catalog.Video, entry source.Entry) bool {
	return time.Since(time.Unix(v.MetadataAt, 0)) < 6*time.Hour && (entry.Title == "" || entry.Title == v.Title)
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
