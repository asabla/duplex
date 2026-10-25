package catalog

import (
	"duplex/internal/source"
)

// Video is the canonical source record, independent of playlist occurrences and Plex keys.
type Video struct {
	ID               string `json:"id"`
	ChannelID        string `json:"channel_id"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	State            string `json:"state"`
	Published        int64  `json:"published"`
	Year             int    `json:"year"`
	Number           int    `json:"number"`
	Exposed          bool   `json:"exposed"`
	Revision         string `json:"revision"`
	Height           int    `json:"height"`
	Fallback         string `json:"fallback,omitempty"`
	Size             int64  `json:"size"`
	Updated          int64  `json:"updated"`
	Preparation      string `json:"preparation"`
	PreparationError string `json:"preparation_error,omitempty"`
	MetadataAt       int64  `json:"metadata_at"`
}

// Prepared describes an exact, verified media revision ready for publication.
type Prepared struct {
	Metadata        source.Metadata
	RequestedHeight int
	Revision        string
	Height          int
	Fallback        string
	Size            int64
}
