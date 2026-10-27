package admin

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"duplex/internal/catalog"
)

func localPageURL(raw, fallback string) string {
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || len(raw) > 8192 {
		return fallback
	}

	switch u.Path {
	case "/channels", "/channel", "/video", "/selection", "/catalogue", "/playlists", "/jobs", "/artwork", "/settings", "/diagnostics":
		return u.RequestURI()
	default:
		return fallback
	}
}

func channelLink(path, id, back string) string {
	return path + "?" + url.Values{"channel": {id}, "back": {localPageURL(back, "/channels")}}.Encode()
}

func (p page) ChannelLink(path string) string { return channelLink(path, p.Channel.ID, p.BackURL) }

func (p page) ChannelRowLink(id string) string        { return channelLink("/channel", id, p.CurrentURL) }
func (p page) ChannelCounts(id string) catalog.Counts { return p.ChannelTotals[id] }
func (p page) Filtered() bool {
	for key, values := range p.Filters {
		if key != "page" && key != "per_page" && key != "sort" && key != "back" && key != "channel" && len(values) > 0 && values[0] != "" {
			return true
		}
	}

	return false
}

func (p page) ClearURL() string {
	if p.Channel.ID != "" {
		return p.ChannelLink(strings.TrimPrefix(p.Page, "/fragments"))
	}

	return strings.TrimPrefix(p.Page, "/fragments")
}

func (p *page) channelContext(r *http.Request) {
	p.CurrentURL = strings.TrimPrefix(r.URL.RequestURI(), "/fragments")

	p.BackURL = localPageURL(r.URL.Query().Get("back"), "/channels")
	if r.URL.Path == "/video" {
		p.VideoBackURL = localPageURL(r.URL.Query().Get("back"), "/catalogue")

		parent, err := url.Parse(p.VideoBackURL)
		if err == nil {
			p.BackURL = localPageURL(parent.Query().Get("back"), "/channels")
		} else {
			p.BackURL = "/channels"
		}
	}

	for _, ch := range p.Status.Config.Config.Channels {
		if ch.ID == r.URL.Query().Get("channel") {
			p.Channel = ch
			break
		}
	}

	for _, ch := range p.Status.Channels {
		if ch.ID == p.Channel.ID {
			p.ChannelInfo = ch
			break
		}
	}
}

func policyFormID(channel, playlist string) string {
	return fmt.Sprintf("policy-%x", sha256.Sum256([]byte(channel+"\x00"+playlist)))[:23]
}

type policyForm struct {
	CSRF, Revision, ID, ChannelID, PlaylistID string
	Mode                                      string
	Summary, EffectiveMode                    string
	Limit                                     int
	Include, Inherited, OOB                   bool
}

func (p page) PolicyForm(channel, playlist string) policyForm {
	ch := p.ChannelSettings[channel]
	selection := ch.Selection()

	f := policyForm{CSRF: p.CSRF, Revision: p.Revision, ID: policyFormID(channel, playlist), ChannelID: channel, PlaylistID: playlist, Mode: selection.Mode, Limit: selection.Limit, Inherited: playlist != ""}
	if ch.Playlists != nil {
		f.Include = ch.Playlists.Enabled
		if override, ok := ch.Playlists.Overrides[playlist]; ok && override.Mode != "inherit" {
			f.Mode = override.Mode
			f.Limit = override.Limit
			f.Inherited = false
		}
	}

	f.EffectiveMode = f.Mode

	f.Summary = selectionText(f.Mode, f.Limit)
	if playlist != "" {
		if f.Inherited {
			f.Summary = "Inherited: " + f.Summary
		} else {
			f.Summary = "Override: " + f.Summary
		}

		if !f.Include {
			f.Summary = "Skipped by channel"
			f.EffectiveMode = "skip"
		}
	}

	if f.Inherited {
		f.Mode = "inherit"
	}

	if f.Limit == 0 {
		f.Limit = 50
	}

	return f
}

type playbackState struct {
	Label, Tone, Detail string
	Retry               bool
}

func playback(v catalog.Video) playbackState {
	if !v.Exposed {
		return playbackState{Label: "Excluded", Tone: "neutral", Detail: "Outside the current library selection"}
	}

	if v.State == "upcoming" || v.State == "active" || v.State == "processing" {
		return playbackState{Label: "Waiting for recording", Tone: "neutral", Detail: "Available after the broadcast has completed processing"}
	}

	if v.State == "unavailable" {
		return playbackState{Label: "Source unavailable", Tone: "danger", Detail: "The source cannot currently provide this recording"}
	}

	if v.Preparation == "ready" && v.Revision != "" && v.Size > 0 {
		return playbackState{Label: "Prepared", Tone: "success", Detail: "Media file prepared; Plex discovery and playback are separate checks"}
	}

	if v.Preparation == "failed" {
		return playbackState{Label: "Needs attention", Tone: "danger", Detail: v.PreparationError, Retry: true}
	}

	labels := map[string]string{"queued": "Queued", "resolving": "Checking source", "preparing": "Preparing media", "waiting": "Waiting for source", "skipped": "Skipped"}

	label := labels[v.Preparation]
	if label == "" {
		label = "Awaiting preparation"
	}

	return playbackState{Label: label, Tone: "info", Detail: "The recording is not ready for playback", Retry: v.Preparation == "discovered" || v.Preparation == ""}
}

func stageLabel(value string) string {
	labels := map[string]string{"": "Discovery", "uploads": "Uploads", "playlists": "Playlists", "complete": "Finished", "running": "In progress", "failed": "Failed", "interrupted": "Interrupted", "recorded": "Upload", "replay-ready": "Completed broadcast", "upcoming": "Scheduled broadcast", "active": "Live broadcast", "processing": "YouTube processing", "discovered": "Discovered", "ready": "Prepared", "queued": "Queued", "resolving": "Checking source", "preparing": "Preparing media", "waiting": "Waiting for source", "skipped": "Skipped", "unavailable": "Unavailable"}
	if label, ok := labels[value]; ok {
		return label
	}

	return strings.ReplaceAll(value, "-", " ")
}

func (p page) VideoLink(id string) string {
	q := url.Values{"id": {id}, "back": {localPageURL(p.CurrentURL, "/catalogue")}}
	if p.Channel.ID != "" {
		q.Set("channel", p.Channel.ID)
	}

	return "/video?" + q.Encode()
}

func selectionText(mode string, limit int) string {
	switch mode {
	case "latest":
		return fmt.Sprintf("Latest %d", limit)
	case "all":
		return "All videos"
	default:
		return "Skipped"
	}
}
