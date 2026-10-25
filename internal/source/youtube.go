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
	"time"

	"duplex/internal/config"
)

const YTDLPVersion = "2026.10.21"
const DenoVersion = "2.9.8"

var ErrUnavailable = errors.New("video is unavailable without credentials")

var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

type Entry struct {
	Group            int     `json:"-"`
	ID               string  `json:"id"`
	Title            string  `json:"title"`
	Timestamp        float64 `json:"timestamp"`
	ReleaseTimestamp float64 `json:"release_timestamp"`
	LiveStatus       string  `json:"live_status"`
}
type Channel struct {
	ID    string
	Title string
	// Entries are complete and newest first within each channel tab (Group).
	Entries []Entry
}
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
}
type Subtitle struct {
	URL string `json:"url"`
	Ext string `json:"ext"`
}
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
func (m Metadata) Date() time.Time {
	if d, e := time.Parse("20060102", m.UploadDate); e == nil {
		return d
	}
	if m.Timestamp > 0 {
		return time.Unix(int64(m.Timestamp), 0).UTC()
	}
	return time.Time{}
}

type SourceAdapter interface {
	ResolveChannel(context.Context, string) (Channel, error)
	Enumerate(context.Context, string) (Channel, error)
	Metadata(context.Context, string) (Metadata, error)
	Resolve(context.Context, string) (Metadata, error)
}
type YouTube struct {
	Executable string
	Deno       string
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
	binary := y.Executable
	if binary == "" {
		binary = "yt-dlp"
	}
	deno := y.Deno
	if deno == "" {
		deno = "deno"
	}
	denoPath, e := exec.LookPath(deno)
	if e != nil {
		return nil, errors.New("Deno is unavailable")
	}
	deno = denoPath
	base := []string{"--ignore-config", "--no-warnings", "--no-progress", "--ignore-no-formats-error", "--no-remote-components", "--no-js-runtimes", "--js-runtimes", "deno:" + deno, "--socket-timeout", "20", "--retries", "1", "--extractor-retries", "1", "--skip-download", "--dump-single-json", "--abort-on-error"}
	cmd := exec.CommandContext(ctx, binary, append(base, args...)...)
	out := &cappedBuffer{limit: 64 << 20}
	cmd.Stdout = out
	stderr := &cappedBuffer{limit: 64 << 10}
	cmd.Stderr = stderr
	if e := cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		message := strings.ToLower(stderr.String())
		for _, marker := range []string{"private video", "members-only", "video unavailable", "video is not available"} {
			if strings.Contains(message, marker) {
				return nil, ErrUnavailable
			}
		}
		return nil, errors.New("YouTube source request failed")
	}
	return out.Bytes(), nil
}
func (y *YouTube) Check(ctx context.Context) error {
	binary := y.Executable
	if binary == "" {
		binary = "yt-dlp"
	}
	b, e := exec.CommandContext(ctx, binary, "--version").Output()
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
	b, e = exec.CommandContext(ctx, deno, "--version").Output()
	if e != nil {
		return errors.New("Deno could not start; check its runtime installation")
	}
	if !strings.HasPrefix(string(b), "deno "+DenoVersion+" ") {
		return errors.New("Deno version differs from pinned release")
	}
	return nil
}

type listing struct {
	ID        string            `json:"id"`
	ChannelID string            `json:"channel_id"`
	Channel   string            `json:"channel"`
	Title     string            `json:"title"`
	Entries   []json.RawMessage `json:"entries"`
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
	groupCounter := 0
	var flatten func([]json.RawMessage, int) error
	flatten = func(items []json.RawMessage, group int) error {
		for _, raw := range items {
			if string(raw) == "null" {
				return errors.New("channel snapshot contains an inaccessible entry")
			}
			var item struct {
				Entry
				Entries []json.RawMessage `json:"entries"`
			}
			if json.Unmarshal(raw, &item) != nil {
				return errors.New("invalid channel entry")
			}
			if item.Entries != nil {
				groupCounter++
				if e := flatten(item.Entries, groupCounter); e != nil {
					return e
				}
			} else if videoID.MatchString(item.ID) {
				item.Entry.Group = group
				result.Entries = append(result.Entries, item.Entry)
			} else {
				return errors.New("incomplete channel snapshot")
			}
		}
		return nil
	}
	if e := flatten(l.Entries, 0); e != nil {
		return Channel{}, e
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
func (y *YouTube) Metadata(ctx context.Context, id string) (Metadata, error) {
	return y.Resolve(ctx, id)
}
func (y *YouTube) Resolve(ctx context.Context, id string) (Metadata, error) {
	if !videoID.MatchString(id) {
		return Metadata{}, errors.New("invalid video ID")
	}
	b, e := y.run(ctx, "--no-playlist", "--", "https://www.youtube.com/watch?v="+id)
	if e != nil {
		return Metadata{}, e
	}
	var m Metadata
	if json.Unmarshal(b, &m) != nil || m.ID != id {
		return Metadata{}, errors.New("invalid video response")
	}
	return m, nil
}

type Candidate struct {
	Video    Format
	Audio    *Format
	Height   int
	Combined bool
}

func Candidates(m Metadata, maxHeight int) []Candidate {
	audio := []Format{}
	videos := []Format{}
	combined := []Format{}
	for _, f := range m.Formats {
		if f.Protocol != "https" || !AllowedURL(f.URL) || f.Ext != "mp4" && f.Ext != "m4a" {
			continue
		}
		if f.VCodec == "none" && (strings.HasPrefix(f.ACodec, "mp4a") || f.ACodec == "opus") {
			audio = append(audio, f)
		} else if f.Height > 0 && f.Height <= maxHeight && (strings.HasPrefix(f.VCodec, "avc") || strings.HasPrefix(f.VCodec, "av01") || strings.HasPrefix(f.VCodec, "vp09")) {
			if f.ACodec == "none" {
				videos = append(videos, f)
			} else if strings.HasPrefix(f.ACodec, "mp4a") {
				combined = append(combined, f)
			}
		}
	}
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

// Only resolver-owned Google media hosts are permitted. Public channel input
// never becomes an arbitrary HTTP or subprocess destination.
func AllowedURL(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, suffix := range []string{"googlevideo.com", "ytimg.com", "ggpht.com", "youtube.com"} {
		if h == suffix || strings.HasSuffix(h, "."+suffix) {
			return true
		}
	}
	return false
}
func HTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 || !AllowedURL(r.URL.String()) {
			return errors.New("source redirect rejected")
		}
		return nil
	}}
}
