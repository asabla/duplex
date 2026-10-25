package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"regexp"

	"duplex/internal/media"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

type Entry struct {
	Path     string
	Media    media.Representation
	Modified int64
}
type Episode struct {
	ChannelID, ChannelTitle, VideoID, Title, Description string
	Year, Number                                         int
	Media                                                media.Representation
	Modified                                             int64
}

type blob struct {
	id   string
	data []byte
}

func (b blob) ID() string  { return b.id }
func (b blob) Size() int64 { return int64(len(b.data)) }
func (b blob) ReadAt(_ context.Context, p []byte, o int64) (int, error) {
	return bytes.NewReader(b.data).ReadAt(p, o)
}

func Entries(episodes []Episode) ([]Entry, error) {
	var entries []Entry
	shows := map[string]bool{}
	seen := map[string]bool{}
	for _, e := range episodes {
		if !safeID.MatchString(e.ChannelID) || !safeID.MatchString(e.VideoID) || e.Year < 1 || e.Number < 1 || e.Media == nil {
			return nil, fmt.Errorf("invalid library episode")
		}
		dir := "Channel-" + e.ChannelID
		if !shows[e.ChannelID] {
			show := struct {
				XMLName xml.Name `xml:"tvshow"`
				Title   string   `xml:"title"`
				ID      string   `xml:"id"`
			}{Title: e.ChannelTitle, ID: "youtube-channel:" + e.ChannelID}
			b, err := xml.MarshalIndent(show, "", "  ")
			if err != nil {
				return nil, err
			}
			p := path.Join(dir, "tvshow.nfo")
			entries = append(entries, Entry{Path: p, Media: blob{id: p, data: append([]byte(xml.Header), b...)}, Modified: e.Modified})
			shows[e.ChannelID] = true
		}
		// Do not append arbitrary source IDs: suffixes such as "-video" can be
		// interpreted as extras by Plex's modern scanner. The assigned episode
		// slot is stable; the independent source identity is stored in NFO.
		base := path.Join(dir, fmt.Sprintf("Season %04d", e.Year), fmt.Sprintf("Channel-%s - S%04dE%04d", e.ChannelID, e.Year, e.Number))
		if seen[base] {
			return nil, fmt.Errorf("duplicate library episode")
		}
		seen[base] = true
		episode := struct {
			XMLName xml.Name `xml:"episodedetails"`
			Title   string   `xml:"title"`
			Plot    string   `xml:"plot"`
			Season  int      `xml:"season"`
			Episode int      `xml:"episode"`
			ID      string   `xml:"id"`
		}{Title: e.Title, Plot: e.Description, Season: e.Year, Episode: e.Number, ID: "youtube-video:" + e.VideoID}
		b, err := xml.MarshalIndent(episode, "", "  ")
		if err != nil {
			return nil, err
		}
		entries = append(entries, Entry{Path: base + ".nfo", Media: blob{id: base + ".nfo", data: append([]byte(xml.Header), b...)}, Modified: e.Modified}, Entry{Path: base + ".mp4", Media: e.Media, Modified: e.Modified})
	}
	return entries, nil
}

func Inode(identity string) uint64 {
	h := sha256.Sum256([]byte(identity))
	return binary.BigEndian.Uint64(h[:8]) | 1<<63
}

// CopyRange is used by mount qualification without manufacturing zero-filled
// placeholders for missing media.
func CopyRange(ctx context.Context, w io.Writer, r media.Representation, offset, length int64) error {
	if offset < 0 || length < 0 || length > r.Size()-offset {
		return io.ErrUnexpectedEOF
	}
	_, err := io.CopyN(w, &media.Seeker{Context: ctx, Source: r, Offset: offset}, length)
	return err
}
