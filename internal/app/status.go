package app

import (
	"context"

	"duplex/internal/cache"
	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/diagnostics"
)

// Status is the compatibility diagnostic snapshot; Summary is intended for polling.
type Status struct {
	Ready           bool                      `json:"ready"`
	MountReady      bool                      `json:"mount_ready"`
	RestartRequired bool                      `json:"restart_required"`
	SourceError     string                    `json:"source_error,omitempty"`
	MountError      string                    `json:"mount_error,omitempty"`
	PlexError       string                    `json:"plex_error,omitempty"`
	MetadataError   string                    `json:"metadata_error,omitempty"`
	MetadataPending int                       `json:"metadata_pending"`
	ConfigError     string                    `json:"config_error,omitempty"`
	PendingJobs     int                       `json:"pending_jobs"`
	Cache           cache.Stats               `json:"cache"`
	Channels        []catalog.Channel         `json:"channels"`
	Videos          []catalog.Video           `json:"videos"`
	Jobs            []catalog.Job             `json:"jobs"`
	Config          config.Snapshot           `json:"configuration"`
	Measurements    []diagnostics.Measurement `json:"measurements,omitempty"`
	Counters        []diagnostics.Counter     `json:"counters,omitempty"`
}

// Status is the compatibility diagnostic snapshot; Summary is intended for polling.
func (a *App) Status(ctx context.Context) (Status, error) {
	s := Status{Config: a.Config.Snapshot(), Cache: a.Cache.Stats(), Measurements: a.Measurements.Snapshot(), Counters: a.Measurements.Counters()}
	var e error
	if s.Channels, e = a.Catalog.Channels(ctx); e != nil {
		return s, e
	}
	if s.Videos, e = a.Catalog.Videos(ctx); e != nil {
		return s, e
	}
	if s.Jobs, e = a.Catalog.Jobs(ctx); e != nil {
		return s, e
	}
	if s.MetadataPending, s.MetadataError, e = a.Catalog.ShowRefreshStatus(ctx); e != nil {
		return s, e
	}
	a.mu.RLock()
	s.SourceError, s.MountError, s.PlexError, s.RestartRequired, s.PendingJobs = a.toolError, a.mountError, a.plexError, a.restart, len(a.queued)
	cacheError := a.cacheError
	s.MountReady = a.mount != nil && a.mount.Ready()
	a.statusVideos(&s)
	a.mu.RUnlock()
	s.ConfigError = s.Config.Error
	if cacheError != "" {
		s.ConfigError = cacheError
	}
	s.Ready = s.MountReady && s.SourceError == "" && s.PlexError == "" && !s.RestartRequired
	return s, nil
}

// statusVideos requires a.mu to be held by the caller.
func (a *App) statusVideos(s *Status) {
	enabled := map[string]bool{}
	for _, ch := range s.Config.Config.Channels {
		enabled[ch.ID] = ch.Enabled
	}
	for i := range s.Videos {
		v := &s.Videos[i]
		if !enabled[v.ChannelID] {
			v.Exposed = false
		}
	}
}
