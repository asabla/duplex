package admin

import (
	"net/url"

	"duplex/internal/app"
	"duplex/internal/catalog"
	"duplex/internal/config"
)

type page struct {
	Page                     string
	CSRF                     string
	Error                    string
	Notice                   string
	ReturnURL                string
	Status                   app.Status
	YAML                     string
	Revision                 string
	Query                    string
	Total, Matches           int
	Pagination               pagination
	ChannelRows              []config.Channel
	ChannelData              []catalog.Channel
	ChannelSettings          map[string]config.Channel
	PlaylistRows             []catalog.PlaylistRow
	Counts                   catalog.Counts
	Filters                  url.Values
	Artwork                  app.ArtworkPreview
	SeasonRows               []catalog.ArtworkSeason
	SeasonPagination         pagination
	CurrentSeasonOnPage      bool
	PlaylistOptions          []catalog.PlaylistRow
	PlaylistFallback         string
	Channel                  config.Channel
	ChannelInfo              catalog.Channel
	ChannelTotals            map[string]catalog.Counts
	CurrentURL, BackURL, Tab string
	VideoBackURL             string
	OOB                      bool
	ErrorFieldID             string
	Video                    catalog.Video
	SelectionEditor          selectionEditor
	SelectionPreview         *catalog.SelectionPreview
	SelectionJSON            string
	VideoChannel             config.Channel
	SelectionConflict        bool
	RecordingSearch          catalog.RecordingSearch
	RecordingQuery           string
	RecordingKinds           map[string]string
	RecordingSearchOpen      bool
}

func (p page) ChannelName(id string) string {
	for _, ch := range p.Status.Channels {
		if ch.ID == id && ch.Title != "" {
			return ch.Title
		}
	}

	return id
}
