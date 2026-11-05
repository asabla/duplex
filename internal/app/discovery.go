package app

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"duplex/internal/config"
	"duplex/internal/source"
)

type discoveryRun struct {
	app     *App
	id      int64
	channel config.Channel
	reader  source.Collections
}

// Sync records one discovery job and reconciles only successfully enumerated windows.
func (a *App) Sync(ctx context.Context, id string) (result error) {
	defer a.Measurements.Begin("sync")()

	work, release, err := a.startDiscovery(ctx)
	if err != nil {
		return err
	}
	defer release()
	defer func() {
		if errors.Is(context.Cause(work), errBackgroundDeferred) {
			result = errors.Join(errBackgroundDeferred, result)
		}
	}()

	ctx = work

	ch, err := a.followed(id)
	if err != nil {
		return err
	}

	before, err := a.projectionTime(ctx, id)
	if err != nil {
		return err
	}

	runID, err := a.Catalog.StartJob(ctx, id)
	if err != nil {
		return err
	}

	run := discoveryRun{app: a, id: runID, channel: ch}

	run.reader, _ = source.CollectionReader(a.Source)
	if run.reader != nil {
		run.reader = admittedCollections{Collections: run.reader, app: a}
	}

	err = run.execute(ctx)
	if err == nil {
		after, failure := a.projectionTime(ctx, id)

		err = failure
		if failure == nil && after != before {
			a.requestRecordingScan(id)
		}
	}

	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer stop()

	if errors.Is(context.Cause(ctx), errBackgroundDeferred) {
		return errors.Join(errBackgroundDeferred, a.Catalog.DeferJob(cleanup, runID))
	}

	finishErr := a.Catalog.FinishJob(cleanup, runID, id, err)

	return errors.Join(err, finishErr)
}

func (a *App) followed(id string) (config.Channel, error) {
	if ch, ok := a.Config.Channel(id); ok && ch.Enabled {
		return ch, nil
	}

	return config.Channel{}, errors.New("channel is not followed or enabled")
}

func (r *discoveryRun) execute(ctx context.Context) error {
	r.app.mu.RLock()
	toolError := r.app.toolError
	r.app.mu.RUnlock()

	if toolError != "" {
		return errors.New(toolError)
	}

	if err := r.app.Catalog.EnsureChannel(ctx, r.channel.ID, ""); err != nil {
		return err
	}

	var (
		cursors []*uploadCursor
		title   string
	)

	if r.reader != nil {
		for _, tab := range []string{"videos", "streams", "shorts"} {
			cursors = append(cursors, &uploadCursor{run: r, scope: source.Scope{ChannelID: r.channel.ID, Tab: tab, Start: 1}})
		}
	} else {
		snapshot, err := r.app.Source.Enumerate(ctx, r.channel.ID)
		if err != nil {
			return err
		}

		if snapshot.ID != r.channel.ID {
			return errors.New("channel identity changed")
		}

		title = snapshot.Title

		groups := map[int][]source.Entry{}
		for _, entry := range snapshot.Entries {
			groups[entry.Group] = append(groups[entry.Group], entry)
		}

		for _, entries := range groups {
			cursors = append(cursors, &uploadCursor{run: r, entries: entries, complete: true})
		}
	}

	if len(r.channel.Unlisted) > 0 {
		explicit := &uploadCursor{run: r, complete: true}
		for _, id := range r.channel.Unlisted {
			explicit.entries = append(explicit.entries, source.Entry{ID: id})
		}

		cursors = append(cursors, explicit)
	}

	if err := r.discoverUploads(ctx, cursors); err != nil {
		return err
	}

	if err := r.app.Catalog.EnsureChannel(ctx, r.channel.ID, title); err != nil {
		return err
	}

	if r.reader != nil {
		return r.playlists(ctx)
	}

	return nil
}

func (r *discoveryRun) current() error {
	ch, err := r.app.followed(r.channel.ID)
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(ch, r.channel) {
		return errors.New("channel settings changed during discovery; refresh queued settings")
	}

	return nil
}

// An empty projection has no visible media and therefore has timestamp zero.
func (a *App) projectionTime(ctx context.Context, channel string) (int64, error) {
	changed, err := a.Catalog.ProjectionChanged(ctx, visibility(a.Config.Snapshot().Config), channel)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}

	return changed, err
}
