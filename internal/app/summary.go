package app

import (
	"context"

	"duplex/internal/catalog"
)

// Summary is used for health checks and small progress regions. Status remains
// the legacy JSON catalogue view; polling never needs its unbounded video array.
func (a *App) Summary(ctx context.Context) (Status, catalog.Counts, error) {
	s := Status{Config: a.Config.Snapshot(), Cache: a.Cache.Stats(), Measurements: a.Measurements.Snapshot()}
	counts, err := a.Catalog.Summary(ctx, visibility(s.Config.Config))
	if err != nil {
		return s, counts, err
	}
	if s.Channels, err = a.Catalog.Channels(ctx); err != nil {
		return s, counts, err
	}
	if s.Jobs, err = a.Catalog.Jobs(ctx); err != nil {
		return s, counts, err
	}
	if s.PendingJobs, err = a.Catalog.PendingPreparations(ctx); err != nil {
		return s, counts, err
	}
	if s.MetadataPending, s.MetadataError, err = a.Catalog.ShowRefreshStatus(ctx); err != nil {
		return s, counts, err
	}
	a.mu.RLock()
	s.SourceError, s.MountError, s.PlexError, s.RestartRequired = a.toolError, a.mountError, a.plexError, a.restart
	s.PendingJobs += len(a.queued)
	s.MountReady = a.mount != nil && a.mount.Ready()
	s.ConfigError = s.Config.Error
	if a.cacheError != "" {
		s.ConfigError = a.cacheError
	}
	a.mu.RUnlock()
	s.Ready = s.MountReady && s.SourceError == "" && s.PlexError == "" && !s.RestartRequired
	return s, counts, nil
}

// Videos returns a filtered database page with current YAML authority applied.
func (a *App) Videos(ctx context.Context, f catalog.VideoFilter) (catalog.VideoPage, error) {
	f.Visibility = visibility(a.Config.Snapshot().Config)
	return a.Catalog.VideosPage(ctx, f)
}
