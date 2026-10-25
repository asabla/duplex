package app

import (
	"context"
	"errors"
	"time"

	"duplex/internal/config"
)

// AddChannel preserves the original numeric-limit API. New clients can use Follow.
func (a *App) AddChannel(ctx context.Context, raw string, limit int, expected string) error {
	if limit == 0 {
		limit = 50
	}
	return a.Follow(ctx, raw, config.Selection{Mode: "latest", Limit: limit}, true, expected)
}

// Follow creates explicit upload and playlist defaults in the authoritative YAML.
func (a *App) Follow(ctx context.Context, raw string, selection config.Selection, include bool, expected string) error {
	_, err := a.FollowChannel(ctx, raw, selection, include, expected)
	return err
}

// FollowChannel follows a channel and returns its resolved identity without another source request.
func (a *App) FollowChannel(ctx context.Context, raw string, selection config.Selection, include bool, expected string) (string, error) {
	if err := selection.ValidateUploads(); err != nil {
		return "", err
	}
	if a.Config.Snapshot().Revision != expected {
		return "", config.ErrConflict
	}
	ctx, stop := context.WithTimeout(ctx, 2*time.Minute)
	defer stop()
	ch, e := a.Source.ResolveChannel(ctx, raw)
	if e != nil {
		return "", e
	}
	snap := a.Config.Snapshot()
	if snap.Revision != expected {
		return "", config.ErrConflict
	}
	for _, c := range snap.Config.Channels {
		if c.ID == ch.ID {
			return "", errors.New("channel is already followed")
		}
	}
	url := "https://www.youtube.com/channel/" + ch.ID
	snap.Config.Channels = append(snap.Config.Channels, config.Channel{ID: ch.ID, URL: url, Limit: 50, Enabled: true, Indexing: &selection, Playlists: &config.PlaylistPolicy{Enabled: include}})
	if e = a.Config.Save(snap.Config, expected); e != nil {
		return "", e
	}
	if e = a.Catalog.EnsureChannel(ctx, ch.ID, ch.Title); e != nil {
		return "", errors.New("settings saved; catalogue channel initialization failed")
	}
	return ch.ID, a.Queue(ch.ID)
}

// RemoveChannel updates YAML and removes the channel from the published library.
func (a *App) RemoveChannel(id, expected string) error {
	s := a.Config.Snapshot()
	channels := []config.Channel{}
	found := false
	for _, ch := range s.Config.Channels {
		if ch.ID == id {
			found = true
		} else {
			channels = append(channels, ch)
		}
	}
	if !found {
		return errors.New("channel is not followed")
	}
	s.Config.Channels = channels
	if e := a.Config.Save(s.Config, expected); e != nil {
		return e
	}
	return a.Publish()
}
