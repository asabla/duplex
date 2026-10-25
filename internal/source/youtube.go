// Package source provides a replaceable process adapter. No user text is passed
// through a shell and no resolver output is sent to logs or diagnostics.
package source

import (
	"context"
	"errors"
	"regexp"
	"time"

	"duplex/internal/config"
)

// Pinned source dependencies are verified before source discovery starts.
const (
	YTDLPVersion = "2026.10.21"
	DenoVersion  = "2.9.8"
)

// ErrUnavailable identifies content which cannot be accessed without credentials.
var ErrUnavailable = errors.New("video is unavailable without credentials")

// ErrMissingCollection is an explicitly absent channel tab, not a failed refresh.
var ErrMissingCollection = errors.New("channel collection does not exist")

var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ResolveChannel resolves a submitted channel URL to stable identity and branding.
func (y *YouTube) ResolveChannel(ctx context.Context, raw string) (Channel, error) {
	u, e := config.ChannelURL(raw)
	if e != nil {
		return Channel{}, e
	}

	b, e := y.run(ctx, "--flat-playlist", "--playlist-end", "1", "--", u)
	if e != nil {
		return Channel{}, e
	}

	return decodeChannel(b)
}

// Enumerate reads a complete channel snapshot for compatibility adapters.
func (y *YouTube) Enumerate(ctx context.Context, id string) (Channel, error) {
	u, e := config.ChannelURL("https://www.youtube.com/channel/" + id)
	if e != nil {
		return Channel{}, e
	}

	b, e := y.run(ctx, "--flat-playlist", "--", u)
	if e != nil {
		return Channel{}, e
	}

	return decodeChannel(b)
}

// Metadata retrieves catalogue fields with fewer player requests. Playback
// sources use Resolve; incomplete catalogue data retries the complete profile.
func (y *YouTube) Metadata(ctx context.Context, id string) (Metadata, error) {
	if !videoID.MatchString(id) {
		return Metadata{}, errors.New("invalid video ID")
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// These documented options omit manifest and JavaScript format work. All
	// required catalogue fields are checked; approximate dates are never used.
	args := "youtube:player_client=web;player_skip=js,configs;skip=hls,dash,translated_subs"

	raw, err := y.run(ctx, "--extractor-args", args, "--no-playlist", "--", "https://www.youtube.com/watch?v="+id)
	if err != nil {
		return Metadata{}, err
	}

	metadata, err := DecodeMetadata(raw, id)
	if err != nil {
		return Metadata{}, err
	}

	if metadataRestricted(metadata) {
		return Metadata{}, ErrUnavailable
	}

	if !completeCatalogueMetadata(metadata) {
		metadata, err = y.Resolve(ctx, id)
		if err != nil {
			return Metadata{}, err
		}

		if metadataRestricted(metadata) {
			return Metadata{}, ErrUnavailable
		}

		if !completeCatalogueMetadata(metadata) {
			return Metadata{}, errors.New("source catalogue metadata is incomplete")
		}
	}

	metadata.Formats = nil
	metadata.Subtitles = nil
	metadata.AutomaticCaptions = nil

	return metadata, nil
}

// Resolve retrieves and validates fresh playback-source candidates.
func (y *YouTube) Resolve(ctx context.Context, id string) (Metadata, error) {
	if !videoID.MatchString(id) {
		return Metadata{}, errors.New("invalid video ID")
	}

	b, e := y.run(ctx, "--extractor-args", "youtube:formats=duplicate", "--no-playlist", "--", "https://www.youtube.com/watch?v="+id)
	if e != nil {
		return Metadata{}, e
	}

	return DecodeMetadata(b, id)
}
