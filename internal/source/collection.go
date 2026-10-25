package source

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
		reader, ok := CollectionReader(observed.SourceAdapter)
		if !ok {
			return nil, false
		}
		return observedCollections{Collections: reader, observed: observed}, true
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
	count, err := y.scanProcess(ctx, args, func(ctx context.Context, scanner *bufio.Scanner) (int, error) {
		result, err := consumeEntries(ctx, scanner, s, consume)
		return result.Count, err
	})
	if count == 0 && s.PlaylistID == "" && errors.Is(err, ErrMissingCollection) {
		return Listing{Complete: true}, nil
	}
	return Listing{Count: count, Complete: err == nil && (s.Count == 0 || count < s.Count)}, err
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

// PlaylistStreamer enumerates root collections without retaining the complete list.
type PlaylistStreamer interface {
	StreamPlaylists(context.Context, string, func([]Playlist) error) error
}

// Playlists returns a compatibility list. The application uses StreamPlaylists.
func (y *YouTube) Playlists(ctx context.Context, id string) ([]Playlist, error) {
	out := []Playlist{}
	err := y.StreamPlaylists(ctx, id, func(batch []Playlist) error { out = append(out, batch...); return nil })
	return out, err
}

// StreamPlaylists enumerates owned collections in bounded batches without media resolution.
func (y *YouTube) StreamPlaylists(ctx context.Context, id string, consume func([]Playlist) error) error {
	origin, err := config.ChannelURL("https://www.youtube.com/channel/" + id)
	if err != nil {
		return err
	}
	count, err := y.scanProcess(ctx, []string{"--flat-playlist", "--lazy-playlist", "--dump-json", "--", origin + "/playlists"}, func(ctx context.Context, scanner *bufio.Scanner) (int, error) {
		return consumePlaylists(ctx, scanner, id, consume)
	})
	if count == 0 && errors.Is(err, ErrMissingCollection) {
		return nil
	}
	return err
}

func consumePlaylists(ctx context.Context, scanner *bufio.Scanner, owner string, consume func([]Playlist) error) (int, error) {
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	count := 0
	batch := make([]Playlist, 0, 25)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		p, err := decodePlaylist(scanner.Bytes(), owner)
		if err != nil {
			return count, err
		}
		count++
		batch = append(batch, p)
		if len(batch) == 25 {
			if err = consume(batch); err != nil {
				return count, err
			}
			batch = make([]Playlist, 0, 25)
		}
	}
	if scanner.Err() != nil {
		return count, errors.New("playlist collection response could not be read")
	}
	if len(batch) > 0 {
		if err := consume(batch); err != nil {
			return count, err
		}
	}
	return count, nil
}

func decodePlaylist(raw []byte, owner string) (Playlist, error) {
	var p struct {
		ID          string         `json:"id"`
		Title       string         `json:"title"`
		Description string         `json:"description"`
		Thumbnails  []ArtworkImage `json:"thumbnails"`
	}
	if json.Unmarshal(raw, &p) != nil || !config.ValidPlaylistID(p.ID) {
		return Playlist{}, errors.New("incomplete playlist listing")
	}
	return Playlist{ID: p.ID, ChannelID: owner, Title: p.Title, Description: p.Description, Thumbnail: largestImage(p.Thumbnails)}, nil
}
