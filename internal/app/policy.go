package app

import (
	"context"
	"errors"

	"duplex/internal/catalog"
	"duplex/internal/config"
)

// UpdatePolicy validates one channel or playlist policy through YAML authority.
func (a *App) UpdatePolicy(channel, playlist, revision string, selection config.Selection, include bool) error {
	snapshot := a.Config.Snapshot()
	found := false
	enabled := false
	for i := range snapshot.Config.Channels {
		ch := &snapshot.Config.Channels[i]
		if ch.ID != channel {
			continue
		}
		found = true
		enabled = ch.Enabled
		if ch.Playlists == nil {
			ch.Playlists = &config.PlaylistPolicy{}
		}
		if playlist == "" {
			ch.Indexing = &selection
			ch.Playlists.Enabled = include
			continue
		}
		if !config.ValidPlaylistID(playlist) {
			return errors.New("invalid playlist ID")
		}
		if ch.Playlists.Overrides == nil {
			ch.Playlists.Overrides = map[string]config.Selection{}
		}
		if selection.Mode == "inherit" {
			delete(ch.Playlists.Overrides, playlist)
		} else {
			ch.Playlists.Overrides[playlist] = selection
		}
	}
	if !found {
		return errors.New("channel is not followed")
	}
	if err := a.Config.Save(snapshot.Config, revision); err != nil {
		return err
	}
	if err := a.Publish(); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	return a.Queue(channel)
}

// Playlists returns a bounded page with inherited and overridden policy values.
func (a *App) Playlists(ctx context.Context, f catalog.PlaylistFilter) (catalog.PlaylistPage, error) {
	for _, ch := range a.Config.Snapshot().Config.Channels {
		policy := ch.Selection()
		p := catalog.ChannelPolicy{ID: ch.ID, Mode: policy.Mode, Limit: policy.Limit}
		if ch.Playlists != nil {
			p.Enabled = ch.Playlists.Enabled
			for id, override := range ch.Playlists.Overrides {
				p.Overrides = append(p.Overrides, catalog.PlaylistOverride{ID: id, Mode: override.Mode, Limit: override.Limit})
			}
		}
		f.Policies = append(f.Policies, p)
	}
	return a.Catalog.PlaylistsPage(ctx, f)
}

// Channels applies setting filters before the database page is read.
func (a *App) Channels(ctx context.Context, f catalog.ChannelFilter, mode, state string) (catalog.ChannelPage, error) {
	for _, ch := range a.Config.Snapshot().Config.Channels {
		if mode != "" && ch.Selection().Mode != mode {
			continue
		}
		if (state == "enabled" && !ch.Enabled) || (state == "paused" && ch.Enabled) {
			continue
		}
		f.IDs = append(f.IDs, ch.ID)
	}
	return a.Catalog.ChannelsPage(ctx, f)
}

func visibility(c config.Config) catalog.Visibility {
	var v catalog.Visibility
	for _, ch := range c.Channels {
		if !ch.Enabled {
			continue
		}
		v.Channels = append(v.Channels, ch.ID)
		if ch.Playlists == nil || !ch.Playlists.Enabled {
			continue
		}
		v.PlaylistChannels = append(v.PlaylistChannels, ch.ID)
		for id, p := range ch.Playlists.Overrides {
			if p.Mode == "skip" {
				v.SkippedPlaylists = append(v.SkippedPlaylists, id)
			}
		}
	}
	return v
}
