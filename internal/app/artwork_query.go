package app

import (
	"context"
	"errors"

	"duplex/internal/catalog"
)

// ArtworkSeasons exposes durable season titles through a bounded shared read.
func (a *App) ArtworkSeasons(ctx context.Context, filter catalog.SeasonFilter) (catalog.SeasonPage, error) {
	if len(filter.Query) > 200 {
		return catalog.SeasonPage{}, errors.New("season filter is too long")
	}
	if _, err := a.artworkChannel(filter.Channel); err != nil {
		return catalog.SeasonPage{}, err
	}
	return a.Catalog.ArtworkSeasons(ctx, filter)
}
