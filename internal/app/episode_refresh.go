package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/library"
	"duplex/internal/plex"
)

func (a *App) recordEpisodeAssets(ctx context.Context, id string, entries []library.Entry) (bool, error) {
	identities := make([]string, 0, len(entries))
	for _, entry := range entries {
		identities = append(identities, entry.Path+"="+entry.Media.ID())
	}

	sort.Strings(identities)
	digest := sha256.Sum256([]byte(strings.Join(identities, "\n")))

	return a.Catalog.RecordEpisodeAssets(ctx, id, hex.EncodeToString(digest[:]))
}

func (a *App) runEpisodeRefresh(ctx context.Context, job catalog.EpisodeRefresh) {
	release, err := a.admitPlexUpdate(ctx)
	if err == nil {
		defer release()

		work, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = a.refreshEpisodeMetadata(work, job)

		cancel()
	}

	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer stop()

	if ctx.Err() != nil || errors.Is(err, errBackgroundDeferred) {
		a.operationFailure("defer episode metadata", a.Catalog.DeferEpisodeRefresh(cleanup, job))
		return
	}

	a.operationFailure("episode metadata refresh", err)

	a.operationFailure("episode metadata result", a.Catalog.FinishEpisodeRefresh(cleanup, job, err))
}

func (a *App) refreshEpisodeMetadata(ctx context.Context, job catalog.EpisodeRefresh) error {
	if !a.automaticUpdatesAllowed() {
		return errBackgroundDeferred
	}

	release, err := a.acquireLibraryUpdate(ctx)
	if err != nil {
		return err
	}
	defer release()

	if a.mount == nil || !a.mount.Ready() {
		return errors.New("library mount is unavailable")
	}

	connection := a.Config.Snapshot().Config.Plex

	client, err := a.plexClientFor(connection)
	if err != nil {
		return err
	}

	if err = client.LibraryHealth(ctx, connection.Section); err != nil {
		return err
	}

	if err = client.WaitIdle(ctx, connection.Section); err != nil {
		return err
	}

	after := ""
	for {
		items, err := a.Catalog.EpisodeRefreshPage(ctx, connection.URL, job.Video, after, 128)
		if err != nil {
			return err
		}

		for _, item := range items {
			if err = a.refreshEpisodeOccurrence(ctx, client, connection, job, item); err != nil {
				return err
			}

			after = item.Occurrence
		}

		if len(items) < 128 {
			return nil
		}
	}
}

func (a *App) metadataRefreshStatus(ctx context.Context) (int, string, error) {
	shows, showError, err := a.Catalog.ShowRefreshStatus(ctx)
	episodes, episodeError, episodeErr := a.Catalog.EpisodeRefreshStatus(ctx)

	failure := showError
	if episodeError != "" {
		failure = episodeError
	}

	return shows + episodes, failure, errors.Join(err, episodeErr)
}

func (a *App) refreshEpisodeOccurrence(ctx context.Context, client *plex.Client, connection config.Plex, job catalog.EpisodeRefresh, item catalog.ObservedEpisode) error {
	if !a.automaticUpdatesAllowed() {
		return errBackgroundDeferred
	}

	if err := client.RefreshEpisode(ctx, connection.Section, item.Key, item.Occurrence); err != nil {
		return err
	}

	return a.Catalog.AppliedEpisodeAssets(ctx, job, connection.URL, item)
}
