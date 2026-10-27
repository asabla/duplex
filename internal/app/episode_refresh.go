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
	"duplex/internal/library"
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
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := a.refreshEpisodeMetadata(work, job.Video)

	cancel()

	if ctx.Err() != nil {
		return
	}

	a.operationFailure("episode metadata refresh", err)

	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer stop()

	a.operationFailure("episode metadata result", a.Catalog.FinishEpisodeRefresh(cleanup, job, err))
}

func (a *App) refreshEpisodeMetadata(ctx context.Context, video string) error {
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

	if err = client.WaitScan(ctx, connection.Section); err != nil {
		return err
	}

	after := ""
	for {
		items, err := a.Catalog.EpisodeRefreshPage(ctx, connection.URL, video, after, 128)
		if err != nil {
			return err
		}

		if len(items) == 0 && after == "" {
			return errors.New("plex episode has not been observed")
		}

		for _, item := range items {
			if err = client.RefreshEpisode(ctx, connection.Section, item.Key, item.Occurrence); err != nil {
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
