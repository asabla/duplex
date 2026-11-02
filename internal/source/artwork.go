package source

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// ArtworkSource can retrieve artwork without resolving playable formats.
type ArtworkSource interface {
	ArtworkURL(context.Context, string) (string, error)
}

// PersistentThumbnail retains only a public image origin and path. Query
// signatures, credentials, fragments, and media-source locators are excluded.
func PersistentThumbnail(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return ""
	}

	host := strings.ToLower(u.Hostname())
	if host != "ytimg.com" && !strings.HasSuffix(host, ".ytimg.com") {
		return ""
	}

	u.RawQuery, u.Fragment = "", ""
	u.ForceQuery = false

	return u.String()
}

// ArtworkReader preserves optional artwork capability through observation.
func ArtworkReader(adapter SourceAdapter) (ArtworkSource, bool) {
	if observed, ok := adapter.(Observed); ok {
		reader, ok := ArtworkReader(observed.SourceAdapter)
		if !ok {
			return nil, false
		}

		return observedArtwork{ArtworkSource: reader, observed: observed}, true
	}

	reader, ok := adapter.(ArtworkSource)

	return reader, ok
}

type observedArtwork struct {
	ArtworkSource
	observed Observed
}

func (s observedArtwork) ArtworkURL(ctx context.Context, id string) (string, error) {
	defer s.observed.elapsed("source.artwork")()
	return s.ArtworkSource.ArtworkURL(s.observed.processContext(ctx, CauseAsset), id)
}

// ArtworkURL uses the catalogue profile without its full-resolution fallback.
// It needs no playable audio, format signatures, or complete catalogue fields.
func (y *YouTube) ArtworkURL(ctx context.Context, id string) (string, error) {
	if !videoID.MatchString(id) {
		return "", errors.New("invalid video ID")
	}

	args := "youtube:player_client=web;player_skip=js,configs;skip=hls,dash,translated_subs"

	raw, err := y.run(ctx, "--extractor-args", args, "--no-playlist", "--", "https://www.youtube.com/watch?v="+id)
	if err != nil {
		return "", err
	}

	var response struct {
		ID        string `json:"id"`
		Thumbnail string `json:"thumbnail"`
	}
	if json.Unmarshal(raw, &response) != nil || response.ID != id {
		return "", errors.New("invalid artwork response")
	}

	thumbnail := PersistentThumbnail(response.Thumbnail)
	if thumbnail == "" {
		return "", errors.New("public source artwork is unavailable")
	}

	return thumbnail, nil
}
