package app

import (
	"context"
	"errors"
	"time"

	"duplex/internal/library"
	"duplex/internal/source"
)

const initialAssetBudget = 250 * time.Millisecond

// assetState bounds all optional asset network work to one active operation.
// App.mu protects channels and the active cancellation; no lock spans I/O.
type assetState struct {
	assetWake   chan struct{}
	assetSlots  chan struct{}
	assetCancel context.CancelCauseFunc
}

func (a *App) assetChannels() (chan struct{}, chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.assetWake == nil {
		a.assetWake = make(chan struct{}, 1)
		a.assetSlots = make(chan struct{}, 1)
	}

	return a.assetWake, a.assetSlots
}

func (a *App) wakeAssets() {
	wake, _ := a.assetChannels()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (a *App) acquireAsset(ctx context.Context) (func(), error) {
	_, slots := a.assetChannels()

	finished := a.Measurements.Begin("assets.wait")
	defer finished()

	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Register after acquiring capacity and recheck protection under the same lock
// as the monitor. A session observed while waiting must prevent new requests.
func (a *App) startAssetWork(ctx context.Context) (context.Context, func(), error) {
	release, err := a.acquireAsset(ctx)
	if err != nil {
		return ctx, nil, err
	}

	work, cancel := context.WithCancelCause(backgroundContext(ctx))
	p := a.playbackConfig()
	a.mu.Lock()
	a.assetCancel = cancel

	if a.playbackStatusLocked(p, time.Now()).BackgroundPaused {
		cancel(errBackgroundDeferred)
	}

	a.mu.Unlock()

	return work, func() {
		a.mu.Lock()
		a.assetCancel = nil
		a.mu.Unlock()
		cancel(nil)
		release()
	}, nil
}

func (a *App) queueRecordingAssets(ctx context.Context, m source.Metadata, initial bool) error {
	if a.PlaybackStatus().BackgroundPaused {
		if initial {
			if err := a.ensureInitialThumbnail(m); err != nil {
				return err
			}
		}

		return a.Catalog.QueueAsset(ctx, m.ID)
	}

	if initial {
		owned, err := a.Catalog.BeginInitialAsset(ctx, m.ID)
		if err != nil {
			return err
		}

		if owned {
			return a.warmInitialAssets(ctx, m)
		}
	}

	if err := a.Catalog.QueueAsset(ctx, m.ID); err != nil {
		return err
	}

	a.wakeAssets()

	return nil
}

func (a *App) warmInitialAssets(ctx context.Context, m source.Metadata) error {
	defer a.Measurements.Begin("assets.initial")()

	err := a.ensureInitialThumbnail(m)
	if err != nil {
		a.operationFailure("initial assets", err)
		return a.Catalog.FinishAsset(ctx, m.ID, err)
	}

	work, stop := context.WithTimeout(ctx, initialAssetBudget)

	work, release, err := a.startAssetWork(work)
	if err == nil {
		defer release()

		err = work.Err()
		if err == nil {
			_, err = a.acquireMetadataAssets(work, m)
		}
	}

	interrupted := work.Err() != nil

	stop()

	if !errors.Is(context.Cause(work), errBackgroundDeferred) {
		a.operationFailure("initial assets", err)
	}

	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()

	if interrupted {
		if err := a.Catalog.RetryInitialAsset(cleanup, m.ID); err != nil {
			return err
		}

		a.wakeAssets()

		return nil
	}

	return a.Catalog.FinishAsset(cleanup, m.ID, err)
}

func (a *App) ensureInitialThumbnail(m source.Metadata) error {
	entries := a.loadAssets(m.ID)
	for _, entry := range entries {
		if entry.Path == ".jpg" {
			return nil
		}
	}

	thumbnail, err := a.fallbackThumbnail(m)
	if err != nil {
		return err
	}

	return a.saveAssetManifest(m.ID, append(entries, thumbnail))
}

func (a *App) acquireMetadataAssets(ctx context.Context, m source.Metadata) ([]library.Entry, error) {
	if !a.Config.Snapshot().Config.FetchCaptions {
		return a.acquireAssets(ctx, m, source.Candidate{})
	}

	candidates := source.Candidates(m, a.Config.Snapshot().Config.MaxHeight)
	if len(candidates) == 0 {
		return nil, errors.New("no supported asset language")
	}

	return a.acquireAssets(ctx, m, candidates[0])
}
