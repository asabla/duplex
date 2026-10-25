package app

import (
	"fmt"
	"path"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/library"
)

func enabledChannels(c config.Config) map[string]config.Channel {
	result := map[string]config.Channel{}
	for _, ch := range c.Channels {
		if ch.Enabled {
			result[ch.ID] = ch
		}
	}
	return result
}

func (a *App) publishedEpisodes(rows []catalog.Episode) []library.Episode {
	channels := enabledChannels(a.Config.Snapshot().Config)
	out := []library.Episode{}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, v := range rows {
		ch, enabled := channels[v.OwnerID]
		if !enabled || (v.PlaylistID != "" && ch.PlaylistSelection(v.PlaylistID).Mode == "skip") {
			continue
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
