// Package source provides a replaceable process adapter. No user text is passed
// through a shell and no resolver output is sent to logs or diagnostics.
package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
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

// SourceAdapter separates discovery metadata from playback-source resolution.
type SourceAdapter interface { //nolint:revive // Keep the architecture interface name explicit at source and application boundaries.
	ResolveChannel(context.Context, string) (Channel, error)
	Enumerate(context.Context, string) (Channel, error)
	Metadata(context.Context, string) (Metadata, error)
	Resolve(context.Context, string) (Metadata, error)
}

// YouTube runs the pinned resolver with separate bounded listing and metadata slots.
type YouTube struct {
	Executable    string
	Deno          string
	init          sync.Once
	metadataSlots chan struct{}
	listingSlots  chan struct{}
}
type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("resolver response too large")
	}
	return b.Buffer.Write(p)
}

func (y *YouTube) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	release, err := y.acquire(ctx, false)
	if err != nil {
		return nil, err
	}
	defer release()
	cmd, err := y.command(ctx, args, true)
	if err != nil {
		return nil, err
	}
	out := &cappedBuffer{limit: 64 << 20}
	cmd.Stdout = out
	stderr := &cappedBuffer{limit: 64 << 10}
	cmd.Stderr = stderr
	if e := cmd.Run(); e != nil {
		return nil, resolverError(ctx, stderr.String())
	}
	return out.Bytes(), nil
}

// Check verifies the resolver, EJS component, and JavaScript runtime contract.
func (y *YouTube) Check(ctx context.Context) error {
	binary := y.Executable
	if binary == "" {
		binary = "yt-dlp"
	}
	b, e := exec.CommandContext(ctx, binary, "--version").Output() //nolint:gosec // G204: The operator selects the pinned resolver; arguments are fixed.
	if e != nil {
		return errors.New("yt-dlp could not start; check its runtime installation")
	}
	if strings.TrimSpace(string(b)) != YTDLPVersion {
		return errors.New("yt-dlp version differs from pinned release")
	}
	deno := y.Deno
	if deno == "" {
		deno = "deno"
	}
	b, e = exec.CommandContext(ctx, deno, "--version").Output() //nolint:gosec // G204: The operator selects the pinned JavaScript runtime; arguments are fixed.
	if e != nil {
		return errors.New("deno could not start; check its runtime installation")
	}
	if !strings.HasPrefix(string(b), "deno "+DenoVersion+" ") {
		return errors.New("deno version differs from pinned release")
	}
	return nil
}

type listing struct {
	ID         string            `json:"id"`
	ChannelID  string            `json:"channel_id"`
	Channel    string            `json:"channel"`
	Title      string            `json:"title"`
	Entries    []json.RawMessage `json:"entries"`
	Thumbnails []ArtworkImage    `json:"thumbnails"`
}

func decodeChannel(b []byte) (Channel, error) {
	var l listing
	if json.Unmarshal(b, &l) != nil {
		return Channel{}, errors.New("invalid channel response")
	}
	id := l.ChannelID
	if id == "" {
		id = l.ID
	}
	if len(id) != 24 || !strings.HasPrefix(id, "UC") {
		return Channel{}, errors.New("source did not return a stable channel ID")
	}
	title := l.Channel
	if title == "" {
		title = l.Title
	}
	result := Channel{ID: id, Title: title}
	result.Avatar, result.Banner = channelImages(l.Thumbnails)
	var err error
	group := 0
	result.Entries, err = flattenChannel(l.Entries, 0, &group)
	if err != nil {
		return Channel{}, err
	}
	seen := map[string]bool{}
	entries := result.Entries[:0]
	for _, e := range result.Entries {
		if !seen[e.ID] {
			entries = append(entries, e)
			seen[e.ID] = true
		}
	}
	result.Entries = entries
	return result, nil
}

// ResolveChannel converts a supported channel URL to its stable owner identity.
func channelImages(images []ArtworkImage) (avatar, banner string) {
	for _, image := range images {
		if !AllowedURL(image.URL) {
			continue
		}
		if strings.Contains(image.ID, "avatar") || (image.Width > 0 && image.Width == image.Height) {
			avatar = image.URL
		}
		if strings.Contains(image.ID, "banner") || (image.Height > 0 && image.Width/image.Height >= 3) {
			banner = image.URL
		}
	}
	return avatar, banner
}

func flattenChannel(items []json.RawMessage, group int, counter *int) ([]Entry, error) {
	var entries []Entry
	for _, raw := range items {
		if string(raw) == "null" {
			return nil, errors.New("channel snapshot contains an inaccessible entry")
		}
		var item struct {
			Entry
			Entries []json.RawMessage `json:"entries"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return nil, errors.New("invalid channel entry")
		}
		switch {
		case item.Entries != nil:
			*counter++
			children, err := flattenChannel(item.Entries, *counter, counter)
			if err != nil {
				return nil, err
			}
			entries = append(entries, children...)
		case videoID.MatchString(item.ID):
			item.Group = group
			entries = append(entries, item.Entry)
		default:
			return nil, errors.New("incomplete channel snapshot")
		}
	}
	return entries, nil
}

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

// Metadata retrieves video lifecycle and publication data through the resolver.
func (y *YouTube) Metadata(ctx context.Context, id string) (Metadata, error) {
	return y.Resolve(ctx, id)
}

// Resolve retrieves and validates fresh playback-source candidates.
func (y *YouTube) Resolve(ctx context.Context, id string) (Metadata, error) {
	if !videoID.MatchString(id) {
		return Metadata{}, errors.New("invalid video ID")
	}
	b, e := y.run(ctx, "--no-playlist", "--", "https://www.youtube.com/watch?v="+id)
	if e != nil {
		return Metadata{}, e
	}
	return DecodeMetadata(b, id)
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

// Candidate pairs a supported video track with original-language audio, or a combined file.
type Candidate struct {
	Video    Format
	Audio    *Format
	Height   int
	Combined bool
}

// Candidates ranks finite supported video and original-language audio combinations.
func Candidates(m Metadata, maxHeight int) []Candidate {
	audio, videos, combined := candidateFormats(m.Formats, maxHeight)
	sort.SliceStable(audio, func(i, j int) bool {
		a, b := audio[i], audio[j]
		ao, bo := strings.Contains(strings.ToLower(a.Note), "original"), strings.Contains(strings.ToLower(b.Note), "original")
		if ao != bo {
			return ao
		}
		if a.LanguagePreference != b.LanguagePreference {
			return a.LanguagePreference > b.LanguagePreference
		}
		aacA, aacB := strings.HasPrefix(a.ACodec, "mp4a"), strings.HasPrefix(b.ACodec, "mp4a")
		if aacA != aacB {
			return aacA
		}
		return a.ABR > b.ABR
	})
	sort.SliceStable(videos, func(i, j int) bool {
		if videos[i].Height != videos[j].Height {
			return videos[i].Height > videos[j].Height
		}
		return videos[i].FPS > videos[j].FPS
	})
	result := []Candidate{}
	if len(audio) > 0 {
		for _, v := range videos {
			result = append(result, Candidate{Video: v, Audio: &audio[0], Height: v.Height})
		}
	}
	for _, v := range combined {
		result = append(result, Candidate{Video: v, Height: v.Height, Combined: true})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Height != result[j].Height {
			return result[i].Height > result[j].Height
		}
		return result[i].Combined
	})
	return result
}

func candidateFormats(formats []Format, maxHeight int) (audio, videos, combined []Format) {
	for _, f := range formats {
		if f.Protocol != "https" || !AllowedURL(f.URL) || (f.Ext != "mp4" && f.Ext != "m4a") {
			continue
		}
		switch {
		case f.VCodec == "none" && (strings.HasPrefix(f.ACodec, "mp4a") || f.ACodec == "opus"):
			audio = append(audio, f)
		case f.Height > 0 && f.Height <= maxHeight && supportedVideoCodec(f.VCodec):
			if f.ACodec == "none" {
				videos = append(videos, f)
			} else if strings.HasPrefix(f.ACodec, "mp4a") {
				combined = append(combined, f)
			}
		}
	}
	return audio, videos, combined
}

func supportedVideoCodec(codec string) bool {
	return strings.HasPrefix(codec, "avc") || strings.HasPrefix(codec, "av01") || strings.HasPrefix(codec, "vp09")
}

// AllowedURL permits only resolver-owned Google media and artwork hosts. Public channel input
// never becomes an arbitrary HTTP or subprocess destination.
func AllowedURL(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "yt3.googleusercontent.com" {
		return true
	}
	for _, suffix := range []string{"googlevideo.com", "ytimg.com", "ggpht.com", "youtube.com"} {
		if h == suffix || strings.HasSuffix(h, "."+suffix) {
			return true
		}
	}
	return false
}

// HTTPClient permits only supported source hosts and checks redirects against the same rules.
func HTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 || !AllowedURL(r.URL.String()) {
			return errors.New("source redirect rejected")
		}
		return nil
	}}
}
