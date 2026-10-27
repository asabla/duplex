package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"duplex/internal/catalog"
)

func (a *App) metadataWorker(ctx context.Context) {
	defer a.wg.Done()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if a.mount == nil || !a.mount.Ready() || a.Config.Snapshot().Config.Plex.URL == "" {
			continue
		}

		a.mu.RLock()
		ordering := a.plexBindingError != ""
		a.mu.RUnlock()

		if ordering {
			continue
		}

		a.refreshMetadataQueues(ctx)
	}
}

func (a *App) refreshMetadataQueues(ctx context.Context) {
	show, err := a.Catalog.NextShowRefresh(ctx)
	if err == nil {
		a.runShowRefresh(ctx, show)
	} else if !errors.Is(err, sql.ErrNoRows) {
		a.operationFailure("show metadata queue", err)
	}

	episode, err := a.Catalog.NextEpisodeRefresh(ctx, a.Config.Snapshot().Config.Plex.URL)
	if err == nil {
		a.runEpisodeRefresh(ctx, episode)
	} else if !errors.Is(err, sql.ErrNoRows) {
		a.operationFailure("episode metadata queue", err)
	}
}

func (a *App) runShowRefresh(ctx context.Context, job catalog.ShowRefresh) {
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := a.refreshShowMetadata(work, job.Channel)

	cancel()

	if ctx.Err() != nil {
		return
	}

	a.operationFailure("show metadata refresh", err)

	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer stop()

	a.operationFailure("show metadata result", a.Catalog.FinishShowRefresh(cleanup, job, err))
}

func (a *App) refreshShowMetadata(ctx context.Context, channel string) error {
	if _, err := a.followed(channel); err != nil {
		return nil
	}

	release, err := a.acquireLibraryUpdate(ctx)
	if err != nil {
		return err
	}
	defer release()

	connection := a.Config.Snapshot().Config.Plex

	client, err := a.plexClientFor(connection)
	if err != nil {
		return err
	}

	if err = client.WaitScan(ctx, connection.Section); err != nil {
		return err
	}

	return client.RefreshShow(ctx, connection.Section, channel)
}
