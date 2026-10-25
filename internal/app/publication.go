package app

import (
	"fmt"
	"path"

	"duplex/internal/catalog"
	"duplex/internal/library"
)

func (a *App) publishedEpisodes(rows []catalog.Episode) []library.Episode {
	c := a.Config.Snapshot().Config
	channels := map[string]bool{}
	playlists := map[string]bool{}
	for _, ch := range c.Channels {
		if !ch.Enabled {
			continue
		}
		channels[ch.ID] = true
		if ch.Playlists != nil && ch.Playlists.Enabled {
			playlists[ch.ID] = true
		}
	}
	out := []library.Episode{}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, v := range rows {
		if !channels[v.OwnerID] || (v.PlaylistID != "" && !playlists[v.OwnerID]) {
			continue
		}
		if v.PlaylistID != "" {
			skip := false
			for _, ch := range c.Channels {
				if ch.ID == v.OwnerID && ch.PlaylistSelection(v.PlaylistID).Mode == "skip" {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
		}
		rep := a.reps[v.Revision]
		if rep == nil {
			continue
		}
		out = append(out, library.Episode{ChannelID: v.OwnerID, ChannelTitle: v.ChannelTitle, VideoID: v.VideoID, Title: v.Title, Description: v.Description, Season: v.Season, Number: v.Number, SeasonTitle: v.SeasonTitle, OccurrenceID: v.OccurrenceID, Media: rep, Modified: v.Updated})
	}
	return out
}

func (a *App) episodeAssets(episodes []library.Episode) []library.Entry {
	var out []library.Entry
	for _, ep := range episodes {
		base := path.Join("Channel-"+ep.ChannelID, fmt.Sprintf("Season %04d", ep.Season), fmt.Sprintf("Channel-%s - S%04dE%04d", ep.ChannelID, ep.Season, ep.Number))
		a.mu.RLock()
		assets := a.assets[ep.VideoID]
		a.mu.RUnlock()
		if len(assets) == 0 {
			assets = a.loadAssets(ep.VideoID)
		}
		for _, asset := range assets {
			entry := asset
			entry.Path = base + asset.Path
			entry.Modified = ep.Modified
			out = append(out, entry)
		}
	}
	return out
}
