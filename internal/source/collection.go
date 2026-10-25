package source

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"duplex/internal/config"
)

// Scope identifies a bounded page of one channel tab or playlist.
type Scope struct {
	ChannelID  string
	PlaylistID string
	Tab        string
	Start      int
	Count      int // Zero means stream the complete remainder.
}

// Listing reports whether the source exhausted the requested collection.
type Listing struct {
	Count    int
	Complete bool
}

// Playlist describes ownership independently from the uploader of its videos.
type Playlist struct {
	ID          string
	ChannelID   string
	Title       string
	Description string
	Thumbnail   string
}

// Collections supplements the source adapter with incremental collection reads.
type Collections interface {
	Entries(context.Context, Scope, func([]Entry) error) (Listing, error)
	Playlists(context.Context, string) ([]Playlist, error)
}

// CollectionReader finds optional collection capabilities through observation.
func CollectionReader(adapter SourceAdapter) (Collections, bool) {
	if observed, ok := adapter.(Observed); ok {
		return CollectionReader(observed.SourceAdapter)
	}
	reader, ok := adapter.(Collections)
	return reader, ok
}

func scopeURL(s Scope) (string, error) {
	if s.PlaylistID != "" {
		if !config.ValidPlaylistID(s.PlaylistID) {
			return "", errors.New("invalid playlist ID")
		}
		return "https://www.youtube.com/playlist?list=" + s.PlaylistID, nil
	}
	u, err := config.ChannelURL("https://www.youtube.com/channel/" + s.ChannelID)
	if err != nil {
		return "", err
	}
	switch s.Tab {
	case "videos", "streams", "shorts":
		return u + "/" + s.Tab, nil
	default:
		return "", errors.New("invalid channel tab")
	}
}

// Entries streams flat entries in small batches, without downloading media.
func (y *YouTube) Entries(ctx context.Context, s Scope, consume func([]Entry) error) (Listing, error) {
	u, err := scopeURL(s)
	if err != nil {
		return Listing{}, err
	}
	args := []string{"--flat-playlist", "--lazy-playlist", "--dump-json", "--playlist-start", fmt.Sprint(max(1, s.Start))}
	if s.Count > 0 {
		args = append(args, "--playlist-end", fmt.Sprint(max(1, s.Start)+s.Count-1))
	}
	args = append(args, "--", u)
	return y.streamEntries(ctx, args, s, consume)
}

func (y *YouTube) streamEntries(ctx context.Context, args []string, s Scope, consume func([]Entry) error) (Listing, error) {
	release, err := y.acquire(ctx, true)
	if err != nil {
		return Listing{}, err
	}
	defer release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command, err := y.command(ctx, args, false)
	if err != nil {
		return Listing{}, err
	}
	stderr := &cappedBuffer{limit: 64 << 10}
	command.Stderr = stderr
	pipe, err := command.StdoutPipe()
	if err != nil {
		return Listing{}, err
	}
	if err = command.Start(); err != nil {
		return Listing{}, errors.Join(err, pipe.Close())
	}
	result, readErr := consumeEntries(ctx, bufio.NewScanner(pipe), s, consume)
	if readErr != nil {
		cancel()
	}
	waitErr := command.Wait()
	if readErr != nil {
		return result, readErr
	}
	if waitErr != nil {
		err = resolverError(ctx, stderr.String())
		if result.Count == 0 && s.PlaylistID == "" && errors.Is(err, ErrMissingCollection) {
			return Listing{Complete: true}, nil
		}
		return result, err
	}
	result.Complete = s.Count == 0 || result.Count < s.Count
	return result, nil
}

func consumeEntries(ctx context.Context, scanner *bufio.Scanner, s Scope, consume func([]Entry) error) (Listing, error) {
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	result := Listing{}
	batch := make([]Entry, 0, 25)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var entry Entry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || !videoID.MatchString(entry.ID) {
			return result, errors.New("incomplete collection entry")
		}
		result.Count++
		entry.Position = max(1, s.Start) + result.Count - 1
		batch = append(batch, entry)
		if len(batch) == 25 {
			if err := consume(batch); err != nil {
				return result, err
			}
			batch = make([]Entry, 0, 25)
		}
	}
	if err := scanner.Err(); err != nil {
		return result, errors.New("collection response could not be read")
	}
	if len(batch) > 0 {
		if err := consume(batch); err != nil {
			return result, err
		}
	}
	return result, nil
}

// Playlists discovers channel-owned playlists without resolving their videos.
func (y *YouTube) Playlists(ctx context.Context, id string) ([]Playlist, error) {
	u, err := config.ChannelURL("https://www.youtube.com/channel/" + id)
	if err != nil {
		return nil, err
	}
	b, err := y.run(ctx, "--flat-playlist", "--", u+"/playlists")
	if err != nil {
		if errors.Is(err, ErrMissingCollection) {
			return []Playlist{}, nil
		}
		return nil, err
	}
	var root struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if json.Unmarshal(b, &root) != nil {
		return nil, errors.New("invalid playlist listing")
	}
	out := []Playlist{}
	for _, raw := range root.Entries {
		var p struct {
			ID          string         `json:"id"`
			Title       string         `json:"title"`
			Description string         `json:"description"`
			Thumbnails  []ArtworkImage `json:"thumbnails"`
		}
		if json.Unmarshal(raw, &p) != nil || !config.ValidPlaylistID(p.ID) {
			return nil, errors.New("incomplete playlist listing")
		}
		out = append(out, Playlist{ID: p.ID, ChannelID: id, Title: p.Title, Description: p.Description, Thumbnail: largestImage(p.Thumbnails)})
	}
	return out, nil
}

func (y *YouTube) command(ctx context.Context, args []string, single bool) (*exec.Cmd, error) {
	binary := y.Executable
	if binary == "" {
		binary = "yt-dlp"
	}
	deno := y.Deno
	if deno == "" {
		deno = "deno"
	}
	path, err := exec.LookPath(deno)
	if err != nil {
		return nil, errors.New("Deno is unavailable")
	}
	base := []string{"--ignore-config", "--no-warnings", "--no-progress", "--ignore-no-formats-error", "--no-remote-components", "--no-js-runtimes", "--js-runtimes", "deno:" + path, "--socket-timeout", "20", "--retries", "1", "--extractor-retries", "1", "--skip-download", "--abort-on-error"}
	if single {
		base = append(base, "--dump-single-json")
	}
	command := exec.CommandContext(ctx, binary, append(base, args...)...)
	command.WaitDelay = 5 * time.Second
	return command, nil
}

func resolverError(ctx context.Context, stderr string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	message := strings.ToLower(stderr)
	for _, tab := range []string{"videos", "streams", "shorts", "playlists"} {
		if strings.Contains(message, "does not have a "+tab+" tab") || strings.Contains(message, "doesn't have a "+tab+" tab") {
			return ErrMissingCollection
		}
	}
	for _, marker := range []string{"private video", "members-only", "video unavailable", "video is not available"} {
		if strings.Contains(message, marker) {
			return ErrUnavailable
		}
	}
	return errors.New("YouTube source request failed")
}

func (y *YouTube) acquire(ctx context.Context, listing bool) (func(), error) {
	y.init.Do(func() { y.metadataSlots = make(chan struct{}, 4); y.listingSlots = make(chan struct{}, 2) })
	slots := y.metadataSlots
	if listing {
		slots = y.listingSlots
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ArtworkImage describes a public channel or playlist image candidate.
type ArtworkImage struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func largestImage(images []ArtworkImage) string {
	best := ""
	area := int64(-1)
	for _, image := range images {
		if !AllowedURL(image.URL) {
			continue
		}
		n := int64(image.Width) * int64(image.Height)
		if n >= area {
			area = n
			best = image.URL
		}
	}
	return best
}
