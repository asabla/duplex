package source

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Entry is a lightweight collection identity with source ordering and optional timestamps.
type Entry struct {
	Group            int     `json:"-"`
	Position         int     `json:"playlist_index"`
	ID               string  `json:"id"`
	Title            string  `json:"title"`
	Timestamp        float64 `json:"timestamp"`
	ReleaseTimestamp float64 `json:"release_timestamp"`
	LiveStatus       string  `json:"live_status"`
}

// Channel identifies the collection owner and its public branding.
type Channel struct {
	ID     string
	Title  string
	Avatar string
	Banner string
	// Entries are complete and newest first within each channel tab (Group).
	Entries []Entry
}

// Format describes a finite source candidate and transient playback locator.
type Format struct {
	ID                 string  `json:"format_id"`
	URL                string  `json:"url"`
	Ext                string  `json:"ext"`
	Protocol           string  `json:"protocol"`
	VCodec             string  `json:"vcodec"`
	ACodec             string  `json:"acodec"`
	Height             int     `json:"height"`
	FPS                float64 `json:"fps"`
	ABR                float64 `json:"abr"`
	Language           string  `json:"language"`
	LanguagePreference float64 `json:"language_preference"`
	Note               string  `json:"format_note"`
	Revision           string  `json:"-"`
	VerifiedSize       int64   `json:"-"`
}

// Subtitle is a transient locator for an available sidecar caption format.
type Subtitle struct {
	URL string `json:"url"`
	Ext string `json:"ext"`
}

// Metadata retrieves video lifecycle and publication data through the resolver.
type Metadata struct {
	ID                string                `json:"id"`
	ChannelID         string                `json:"channel_id"`
	Channel           string                `json:"channel"`
	Title             string                `json:"title"`
	Description       string                `json:"description"`
	UploadDate        string                `json:"upload_date"`
	Timestamp         float64               `json:"timestamp"`
	Duration          float64               `json:"duration"`
	LiveStatus        string                `json:"live_status"`
	Availability      string                `json:"availability"`
	Language          string                `json:"language"`
	Thumbnail         string                `json:"thumbnail"`
	Formats           []Format              `json:"formats"`
	Subtitles         map[string][]Subtitle `json:"subtitles"`
	AutomaticCaptions map[string][]Subtitle `json:"automatic_captions"`
}

// SourceAdapter separates discovery metadata from playback-source resolution.
type SourceAdapter interface { //nolint:revive // Keep the architecture interface name explicit at source and application boundaries.
	ResolveChannel(context.Context, string) (Channel, error)
	Enumerate(context.Context, string) (Channel, error)
	Metadata(context.Context, string) (Metadata, error)
	Resolve(context.Context, string) (Metadata, error)
}

// State separates completed recordings from active, processing, and inaccessible content.
func (m Metadata) State() string {
	switch m.LiveStatus {
	case "is_upcoming":
		return "upcoming"
	case "is_live":
		return "active"
	case "post_live":
		return "processing"
	}
	if (m.Availability != "" && m.Availability != "public" && m.Availability != "unlisted") || m.Duration <= 0 {
		return "unavailable"
	}
	if m.LiveStatus == "was_live" {
		return "replay-ready"
	}
	if m.LiveStatus == "not_live" || m.LiveStatus == "" {
		return "recorded"
	}
	return "unavailable"
}

// Date uses publication time when available and falls back to the source upload date.
func (m Metadata) Date() time.Time {
	if m.Timestamp > 0 {
		return time.Unix(int64(m.Timestamp), 0).UTC()
	}
	if d, err := time.Parse("20060102", m.UploadDate); err == nil {
		return d
	}
	return time.Time{}
}

func metadataRestricted(m Metadata) bool {
	return m.Availability != "" && m.Availability != "public" && m.Availability != "unlisted"
}

func completeCatalogueMetadata(m Metadata) bool {
	if m.ChannelID == "" || m.Title == "" {
		return false
	}
	switch m.State() {
	case "upcoming", "active", "processing":
		return true
	case "recorded", "replay-ready":
		return !m.Date().IsZero()
	default:
		return false
	}
}

// DecodeMetadata validates a trusted resolver response and its asset-version evidence.
func DecodeMetadata(b []byte, id string) (Metadata, error) {
	var m Metadata
	if json.Unmarshal(b, &m) != nil || m.ID != id {
		return Metadata{}, errors.New("invalid video response")
	}
	for i := range m.Formats {
		f := &m.Formats[i]
		f.Revision, f.VerifiedSize = assetVersion(f.URL, f.ID)
	}
	return m, nil
}
