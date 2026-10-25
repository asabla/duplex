package config

import (
	"errors"
	"net/url"
	"strings"
)

// ChannelURL validates and canonicalizes a supported public YouTube channel URL.
func ChannelURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || !channelOrigin(u) {
		return "", errors.New("enter an HTTPS YouTube channel URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", errors.New("enter a channel URL")
	}
	if parts[0] == "channel" {
		return channelIDURL(parts)
	}
	return channelHandleURL(parts)
}

func channelOrigin(u *url.URL) bool {
	if u.User != nil || u.Scheme != "https" || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "www.youtube.com" || host == "youtube.com"
}

func channelHandleURL(parts []string) (string, error) {
	if len(parts[0]) < 2 || !strings.HasPrefix(parts[0], "@") {
		return "", errors.New("use a channel ID or handle URL")
	}
	if len(parts) > 2 || (len(parts) == 2 && !channelTab(parts[1])) {
		return "", errors.New("unsupported channel URL")
	}
	canonical := url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/" + parts[0]}
	return canonical.String(), nil
}

func channelIDURL(parts []string) (string, error) {
	if len(parts) < 2 || !validID(parts[1]) {
		return "", errors.New("invalid channel ID")
	}
	if len(parts) > 3 || (len(parts) == 3 && !channelTab(parts[2])) {
		return "", errors.New("unsupported channel URL")
	}
	return "https://www.youtube.com/channel/" + parts[1], nil
}

func channelTab(tab string) bool {
	switch tab {
	case "videos", "streams", "shorts", "playlists":
		return true
	default:
		return false
	}
}
