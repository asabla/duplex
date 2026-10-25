package plex

import (
	"net/url"
	"strings"
)

// SourceID extracts the canonical video ID from a year episode NFO GUID.
func SourceID(guid string) string {
	u, e := url.Parse(guid)
	if e != nil || u.Scheme != "tv.plex.agents.nfo.series" || u.Host != "episode" {
		return ""
	}
	s, e := url.PathUnescape(u.EscapedPath())
	if e != nil {
		return ""
	}
	const prefix = "/youtube-video:"
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return ""
}

// OccurrenceID includes playlist scope so shared videos keep separate progress.
func OccurrenceID(guid string) string {
	u, err := url.Parse(guid)
	if err != nil || u.Scheme != "tv.plex.agents.nfo.series" || u.Host != "episode" {
		return ""
	}
	identity, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return ""
	}
	identity = strings.TrimPrefix(identity, "/")
	if strings.HasPrefix(identity, "youtube-video:") || strings.HasPrefix(identity, "youtube-playlist:") {
		return identity
	}
	return ""
}
