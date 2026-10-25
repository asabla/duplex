package app

import (
	"context"

	"duplex/internal/catalog"
)

// ChannelCounts returns bounded counts using the current YAML selection authority.
func (a *App) ChannelCounts(ctx context.Context, ids []string) (map[string]catalog.Counts, error) {
	return a.Catalog.ChannelCounts(ctx, ids, visibility(a.Config.Snapshot().Config))
}

// Video reads one exact recording with current selection authority applied.
func (a *App) Video(ctx context.Context, id string) (catalog.Video, error) {
	v, err := a.Catalog.Video(ctx, id)
	if err != nil {
		return v, err
	}

	v.Exposed, err = a.Catalog.IsSelected(ctx, id, visibility(a.Config.Snapshot().Config))

	return v, err
}
