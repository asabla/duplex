package config

import (
	"errors"
	"regexp"
	"strings"
)

// Selection defines an explicit finite or complete collection policy.
type Selection struct {
	Mode  string `yaml:"mode" json:"mode"`
	Limit int    `yaml:"limit,omitempty" json:"limit,omitempty"`
}

// PlaylistPolicy applies a channel default and optional per-playlist overrides.
type PlaylistPolicy struct {
	Enabled   bool                 `yaml:"enabled" json:"enabled"`
	Overrides map[string]Selection `yaml:"overrides,omitempty" json:"overrides,omitempty"`
}

// Artwork contains durable asset references, independent of remote image URLs.
type Artwork struct {
	Poster  string            `yaml:"poster,omitempty" json:"poster,omitempty"`
	Seasons map[string]string `yaml:"seasons,omitempty" json:"seasons,omitempty"`
}

// DefaultSelection is the policy for newly followed channels and their playlists.
func DefaultSelection() Selection { return Selection{Mode: "latest", Limit: 50} }

// Selection returns the effective upload policy, including legacy numeric limits.
func (c Channel) Selection() Selection {
	if c.Indexing != nil {
		return *c.Indexing
	}

	return Selection{Mode: "latest", Limit: c.Limit}
}

// PlaylistSelection resolves one playlist's override and channel inheritance.
func (c Channel) PlaylistSelection(id string) Selection {
	if c.Playlists == nil || !c.Playlists.Enabled {
		return Selection{Mode: "skip"}
	}

	if p, ok := c.Playlists.Overrides[id]; ok && p.Mode != "inherit" {
		return p
	}

	return c.Selection()
}

func (s Selection) validate(override bool) error {
	switch s.Mode {
	case "latest":
		if s.Limit < 1 {
			return errors.New("latest mode requires a positive video count")
		}
	case "all":
		if s.Limit != 0 {
			return errors.New("all mode does not take a video count")
		}
	case "skip", "inherit":
		if !override || s.Limit != 0 {
			return errors.New("invalid playlist override")
		}
	default:
		return errors.New("indexing mode must be latest or all")
	}

	return nil
}

func (c Channel) validatePolicy() error {
	if err := c.validateContent(); err != nil {
		return err
	}

	if err := c.Selection().validate(false); err != nil {
		return err
	}

	if c.Playlists != nil {
		for id, p := range c.Playlists.Overrides {
			if !ValidPlaylistID(id) {
				return errors.New("invalid playlist ID")
			}

			if err := p.validate(true); err != nil {
				return err
			}
		}
	}

	if c.Artwork.Poster != "" && !ValidArtworkReference(c.Artwork.Poster) {
		return errors.New("invalid show artwork reference")
	}

	for scope, ref := range c.Artwork.Seasons {
		if !validSeasonScope(scope) || !ValidArtworkReference(ref) {
			return errors.New("invalid season artwork reference")
		}
	}

	return nil
}

var (
	playlistIDPattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{10,100}$`)
	artworkReferencePattern = regexp.MustCompile(`^[a-f0-9]{64}\.jpg$`)
	yearScopePattern        = regexp.MustCompile(`^year:[1-9][0-9]{3}$`)
)

// ValidPlaylistID checks an opaque YouTube playlist identity, not a URL.
func ValidPlaylistID(id string) bool { return playlistIDPattern.MatchString(id) }

// ValidArtworkReference accepts only content-addressed JPEG names.
func ValidArtworkReference(ref string) bool { return artworkReferencePattern.MatchString(ref) }

func validSeasonScope(scope string) bool {
	if yearScopePattern.MatchString(scope) {
		return true
	}

	return strings.HasPrefix(scope, "playlist:") && ValidPlaylistID(strings.TrimPrefix(scope, "playlist:"))
}

// ValidateUploads checks a channel's finite or complete upload selection.
func (s Selection) ValidateUploads() error { return s.validate(false) }
