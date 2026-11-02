package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"duplex/internal/cache"
	"duplex/internal/catalog"
	"duplex/internal/media"
)

type layoutLease struct {
	rep   media.Representation
	used  time.Time
	bytes int64
}

// A filesystem handle retains the requested durable identity. Opening it does
// not resolve a locator or mark a viewer. The first read loads only local layout.
type lazyEpisode struct {
	app     *App
	episode catalog.Episode
	mu      sync.Mutex
	rep     media.Representation
}

func (r *lazyEpisode) ID() string  { return r.episode.Revision }
func (r *lazyEpisode) Size() int64 { return r.episode.Size }
func (r *lazyEpisode) ReadAt(ctx context.Context, p []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	if offset < 0 {
		return 0, errors.New("negative offset")
	}

	if len(p) == 0 {
		return 0, nil
	}

	if offset >= r.Size() {
		return 0, io.EOF
	}

	r.mu.Lock()
	if r.rep == nil {
		rep, err := r.app.localLayout(ctx, r.episode)
		if err != nil {
			r.mu.Unlock()
			return 0, err
		}

		r.rep = rep
	}

	rep := r.rep
	r.mu.Unlock()

	return rep.ReadAt(ctx, p, offset)
}

func (a *App) localLayout(ctx context.Context, episode catalog.Episode) (media.Representation, error) {
	a.mu.Lock()
	if entry, ok := a.layouts[episode.Revision]; ok {
		entry.used = time.Now()
		a.layouts[episode.Revision] = entry
		a.mu.Unlock()
		a.Measurements.Count("layout.hit", 1)

		return entry.rep, nil
	}

	if a.layoutSlots == nil {
		a.layoutSlots = make(chan struct{}, 1)
	}

	slots := a.layoutSlots
	a.mu.Unlock()

	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	defer func() { <-slots }()
	// Check again after the bounded parser slot. Concurrent opens share the
	// result even when they use separate filesystem handles.
	a.mu.RLock()
	entry, ok := a.layouts[episode.Revision]
	a.mu.RUnlock()

	if ok {
		return entry.rep, nil
	}

	return a.loadLocalLayout(episode)
}

func (a *App) loadLocalLayout(episode catalog.Episode) (media.Representation, error) {
	defer a.Measurements.Begin("layout.load")()

	a.Measurements.Count("layout.miss", 1)
	path := filepath.Join(a.original.IndexDir, episode.Revision+".mp4-index.json")

	layout, err := media.LoadLayout(path)
	if errors.Is(err, os.ErrNotExist) {
		// Combined MP4 files and old/missing indexes have no generated header.
		// Their cached original bytes can still satisfy reads before live binding.
		return &lazyTrack{app: a, episode: episode, id: episode.Revision, size: episode.Size, combined: true}, nil
	}

	if err != nil {
		return nil, err
	}

	if layout.ID() != episode.Revision || layout.Size() != episode.Size {
		return nil, media.ErrChanged
	}

	if info, failure := os.Stat(path); failure == nil {
		a.Measurements.Count("layout.disk_bytes", info.Size())
	}

	layout.BindLayout(func(id string, size int64) media.Representation {
		return &lazyTrack{app: a, episode: episode, id: id, size: size}
	})
	a.retainLayout(layout)

	return layout, nil
}

func (a *App) retainLayout(layout *media.VirtualMP4) {
	cost := int64(cap(layout.Header)) + int64(cap(layout.Spans))*160 + 32<<10
	if cost > runtimeIndexBytes {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.layouts == nil {
		a.layouts = map[string]layoutLease{}
	}

	if old, ok := a.layouts[layout.ID()]; ok {
		a.layoutBytes -= old.bytes
	}

	a.layouts[layout.ID()] = layoutLease{rep: layout, used: time.Now(), bytes: cost}

	a.layoutBytes += cost
	a.trimLayoutsLocked()
}

// The shared count and byte budget covers bound indexes and detached layouts.
func (a *App) trimLayoutsLocked() {
	for len(a.layouts) > 0 && (len(a.layouts)+len(a.reps) > runtimeRepresentations || a.layoutBytes+a.runtimeIndexCost() > runtimeIndexBytes) {
		oldest := ""
		for id, entry := range a.layouts {
			if oldest == "" || entry.used.Before(a.layouts[oldest].used) {
				oldest = id
			}
		}

		a.layoutBytes -= a.layouts[oldest].bytes
		delete(a.layouts, oldest)
		a.Measurements.Count("layout.eviction", 1)
	}
}

type lazyTrack struct {
	app      *App
	episode  catalog.Episode
	id       string
	size     int64
	combined bool
}

func (r *lazyTrack) ID() string  { return r.id }
func (r *lazyTrack) Size() int64 { return r.size }
func (r *lazyTrack) ReadAt(ctx context.Context, p []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("negative offset")
	}

	if len(p) == 0 {
		return 0, nil
	}

	if r.size == 0 || r.app.Cache == nil {
		return r.readLive(ctx, p, offset)
	}

	if offset >= r.size {
		return 0, io.EOF
	}

	wanted := len(p)
	p = p[:min(int64(len(p)), r.size-offset)]

	n := 0
	for n < len(p) {
		start := (offset / media.ChunkSize) * media.ChunkSize
		length := int(min(int64(media.ChunkSize), r.size-start))

		data, release, err := r.app.Cache.Lookup(ctx, r.id, start, length)
		if err != nil {
			if !errors.Is(err, cache.ErrMiss) {
				return n, err
			}

			r.app.Measurements.Count("payload.cache_miss", 1)
			count, failure := r.readLive(ctx, p[n:], offset)

			if failure == nil && n+count < wanted {
				failure = io.EOF
			}

			return n + count, failure
		}

		r.app.Measurements.Count("payload.cache_hit", 1)

		count := copy(p[n:], data[offset-start:])

		release()

		n += count
		offset += int64(count)
	}

	if n < wanted {
		return n, io.EOF
	}

	return n, nil
}

func (r *lazyTrack) readLive(ctx context.Context, p []byte, offset int64) (int, error) {
	rep, err := r.app.bindEpisode(ctx, r.episode)
	if err != nil {
		return 0, err
	}

	if r.combined {
		return rep.ReadAt(ctx, p, offset)
	}

	index, ok := rep.(*media.VirtualMP4)
	if !ok {
		return 0, media.ErrChanged
	}

	return index.ReadSource(ctx, r.id, p, offset)
}
