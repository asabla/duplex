package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"duplex/internal/cache"
	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/diagnostics"
	"duplex/internal/library"
	"duplex/internal/media"
	"duplex/internal/source"
)

// Open initializes storage and starts bounded workers under the supplied context.
func Open(ctx context.Context, m *config.Manager, adapter source.SourceAdapter, noMount bool) (*App, error) {
	return OpenWithMountSocket(ctx, m, adapter, noMount, "")
}

// OpenWithMountSocket uses an isolated mount helper when a socket is supplied.
func OpenWithMountSocket(ctx context.Context, m *config.Manager, adapter source.SourceAdapter, noMount bool, socket string) (*App, error) {
	c := m.Snapshot().Config
	for _, p := range []string{c.DataDir, c.IndexDir} {
		if e := os.MkdirAll(p, 0o700); e != nil {
			return nil, e
		}
	}

	store, e := cache.New(c.CacheDir, int64(c.CacheGiB)<<30)
	if e != nil {
		return nil, e
	}

	db, e := catalog.OpenContext(ctx, filepath.Join(c.DataDir, "catalogue.sqlite"))
	if e != nil {
		return nil, errors.Join(e, store.Close())
	}

	ctx, cancel := context.WithCancel(ctx)

	a := &App{
		Measurements: &diagnostics.Recorder{},
		Config:       m,
		Catalog:      db,
		Cache:        store,
		Source:       adapter,
		schedulerState: schedulerState{
			queued:    map[string]bool{},
			attempted: map[string]time.Time{},
			jobs:      make(chan string, 64),
		},
		preparationState: preparationState{prepareWake: make(chan struct{}, 2)},
		libraryState:     libraryState{scanWake: make(chan struct{}, 1)},
		representationState: representationState{
			reps:     map[string]media.Representation{},
			metadata: map[string]metadataLease{},
			leases:   map[string]time.Time{},
		},
		ctx:      ctx,
		cancel:   cancel,
		original: c,
	}
	if checker, ok := adapter.(interface{ Check(context.Context) error }); ok {
		checkCtx, stop := context.WithTimeout(ctx, 15*time.Second)
		e = checker.Check(checkCtx)

		stop()

		if e != nil {
			a.toolError = e.Error()
		}
	}

	a.Source = source.Observed{SourceAdapter: adapter, Record: a.Measurements.Add}

	if !noMount {
		if socket == "" {
			a.mount, e = library.MountProvider(c.Mountpoint, catalogueFS{app: a})
		} else {
			a.mount, e = library.MountProviderWithHelper(c.Mountpoint, catalogueFS{app: a}, socket)
		}

		if e != nil {
			return nil, errors.Join(e, a.Close())
		}
	} else {
		a.mountError = "mount disabled; administration test mode"
	}

	if e = a.Publish(); e != nil {
		return nil, errors.Join(e, a.Close())
	}

	if e = a.Catalog.ResetPreparations(ctx, visibility(c)); e != nil {
		return nil, errors.Join(e, a.Close())
	}

	a.startWorkers(ctx, c.Workers)

	return a, nil
}

// MountDone reports loss of the isolated mount helper, when configured.
func (a *App) MountDone() <-chan struct{} {
	if a.mount == nil {
		return nil
	}

	return a.mount.Done()
}

// Close cancels workers before closing the mount, cache, and catalogue.
func (a *App) Close() error {
	a.cancel()
	a.wg.Wait()

	var mountErr error
	if a.mount != nil {
		mountErr = a.mount.Close()
	}

	e := a.Cache.Close()
	e2 := a.Catalog.Close()

	return errors.Join(mountErr, e, e2)
}

// startWorkers registers each worker before starting it; Close waits for this group.
func (a *App) startWorkers(ctx context.Context, workers int) {
	for range 2 {
		a.wg.Add(1)
		go a.preparationWorker(ctx)
	}

	a.wg.Add(1)
	go a.scanWorker(ctx)

	a.wg.Add(1)
	go a.assetWorker(ctx)

	a.wg.Add(1)
	go a.artworkWorker(ctx)

	a.wg.Add(1)
	go a.metadataWorker(ctx)

	for i := 0; i < workers; i++ {
		a.wg.Add(1)
		go a.worker(ctx)
	}

	a.wg.Add(1)
	go a.schedule(ctx)
}
