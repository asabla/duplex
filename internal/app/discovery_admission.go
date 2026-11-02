package app

import (
	"context"

	"duplex/internal/source"
)

// Never retain a resolver slot across a collection callback: callbacks can need
// their own metadata slot. Pause at each source call and bounded result batch.
type admittedCollections struct {
	source.Collections
	app *App
}

func (c admittedCollections) Entries(ctx context.Context, scope source.Scope, consume func([]source.Entry) error) (source.Listing, error) {
	if err := c.app.waitBackground(ctx); err != nil {
		return source.Listing{}, err
	}

	return c.Collections.Entries(ctx, scope, func(entries []source.Entry) error {
		if err := c.app.waitBackground(ctx); err != nil {
			return err
		}

		return consume(entries)
	})
}

func (c admittedCollections) Playlists(ctx context.Context, id string) ([]source.Playlist, error) {
	if err := c.app.waitBackground(ctx); err != nil {
		return nil, err
	}

	return c.Collections.Playlists(ctx, id)
}

func (c admittedCollections) StreamPlaylists(ctx context.Context, id string, consume func([]source.Playlist) error) error {
	if err := c.app.waitBackground(ctx); err != nil {
		return err
	}

	admitted := func(items []source.Playlist) error {
		if err := c.app.waitBackground(ctx); err != nil {
			return err
		}

		return consume(items)
	}
	if stream, ok := c.Collections.(source.PlaylistStreamer); ok {
		return stream.StreamPlaylists(ctx, id, admitted)
	}

	items, err := c.Collections.Playlists(ctx, id)
	if err != nil {
		return err
	}

	return admitted(items)
}
