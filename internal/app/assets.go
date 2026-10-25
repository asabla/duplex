package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"duplex/internal/artwork"

	"duplex/internal/config"
	"duplex/internal/library"
	"duplex/internal/source"
)

func httpRequest(ctx context.Context, raw string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
}

func (a *App) acquireAssets(ctx context.Context, m source.Metadata, c source.Candidate) ([]library.Entry, error) {
	existing := a.loadAssets(m.ID)
	assets := map[string]library.Entry{}
	for _, entry := range existing {
		assets[entry.Path] = entry
	}
	thumbnail, thumbnailErr := a.thumbnailAsset(ctx, m)
	if thumbnailErr == nil {
		assets[thumbnail.Path] = thumbnail
	} else if assets[".jpg"].Media == nil {
		fallback, err := a.fallbackThumbnail(m)
		if err != nil {
			return existing, errors.Join(thumbnailErr, err)
		}
		assets[fallback.Path] = fallback
	}
	caption, captionErr := a.captionAsset(ctx, m, subtitleLanguage(m, c))
	if caption.Media != nil {
		assets[caption.Path] = caption
	}
	out := make([]library.Entry, 0, len(assets))
	for _, entry := range assets {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if err := a.saveAssetManifest(m.ID, out); err != nil {
		return existing, errors.Join(thumbnailErr, captionErr, err)
	}
	return out, errors.Join(thumbnailErr, captionErr)
}

func (a *App) thumbnailAsset(ctx context.Context, m source.Metadata) (library.Entry, error) {
	if m.Thumbnail == "" {
		return library.Entry{}, errors.New("source thumbnail is missing")
	}
	data, err := fetchAsset(ctx, m.Thumbnail)
	if err != nil {
		return library.Entry{}, err
	}
	picture, err := artwork.DecodeSource(data)
	if err != nil {
		return library.Entry{}, err
	}
	return a.encodeThumbnail(m.ID, picture)
}

func (a *App) fallbackThumbnail(m source.Metadata) (library.Entry, error) {
	hash := sha256.Sum256([]byte(m.ChannelID))
	picture := image.NewRGBA(image.Rect(0, 0, 480, 270))
	draw.Draw(picture, picture.Bounds(), &image.Uniform{C: color.RGBA{hash[0]/2 + 32, hash[1]/2 + 32, hash[2]/2 + 32, 255}}, image.Point{}, draw.Src)
	return a.encodeThumbnail(m.ID, picture)
}

func (a *App) encodeThumbnail(id string, picture image.Image) (library.Entry, error) {
	var data bytes.Buffer
	if err := jpeg.Encode(&data, picture, &jpeg.Options{Quality: 85}); err != nil {
		return library.Entry{}, err
	}
	return a.saveAsset(id, ".jpg", data.Bytes())
}

func (a *App) saveAssetManifest(id string, entries []library.Entry) error {
	if !assetID.MatchString(id) {
		return errors.New("invalid asset identity")
	}
	records := []assetRecord{}
	for _, entry := range entries {
		file, ok := entry.Media.(localAsset)
		if !ok {
			return errors.New("optional assets must use durable files")
		}
		records = append(records, assetRecord{Suffix: entry.Path, File: filepath.Base(file.file), Size: file.size})
	}
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return config.AtomicWrite(filepath.Join(a.original.DataDir, "assets", id+".json"), data, 0o600)
}

func subtitleLanguage(m source.Metadata, c source.Candidate) string {
	if c.Audio != nil && c.Audio.Language != "" {
		return c.Audio.Language
	}
	if m.Language != "" {
		return m.Language
	}
	if c.Video.Language != "" {
		return c.Video.Language
	}
	original := []string{}
	for code := range m.AutomaticCaptions {
		if strings.HasSuffix(code, "-orig") {
			original = append(original, strings.TrimSuffix(code, "-orig"))
		}
	}
	sort.Strings(original)
	if len(original) > 0 {
		return original[0]
	}
	return ""
}

func subtitleTracks(m source.Metadata, language string) []source.Subtitle {
	if tracks := m.Subtitles[language]; len(tracks) > 0 {
		return tracks
	}
	keys := []string{}
	for key := range m.Subtitles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.Split(key, "-")[0] == strings.Split(language, "-")[0] {
			return m.Subtitles[key]
		}
	}
	if tracks := m.AutomaticCaptions[language+"-orig"]; len(tracks) > 0 {
		return tracks
	}
	return m.AutomaticCaptions[language]
}

var subtitleCode = regexp.MustCompile(`^[A-Za-z0-9-]{1,25}$`)

func (a *App) captionAsset(ctx context.Context, m source.Metadata, language string) (library.Entry, error) {
	if language == "" {
		return library.Entry{}, nil
	}
	if !subtitleCode.MatchString(language) {
		return library.Entry{}, errors.New("unsupported subtitle language code")
	}
	tracks := subtitleTracks(m, language)
	var failures []error
	for _, ext := range []string{"srt", "vtt"} {
		for _, track := range tracks {
			if track.Ext != ext {
				continue
			}
			entry, err := a.fetchCaption(ctx, m.ID, language, track)
			if err == nil {
				return entry, nil
			}
			failures = append(failures, err)
		}
	}
	return library.Entry{}, errors.Join(failures...)
}

func (a *App) fetchCaption(ctx context.Context, id, language string, track source.Subtitle) (library.Entry, error) {
	data, err := fetchAsset(ctx, track.URL)
	if err != nil {
		return library.Entry{}, err
	}
	if track.Ext == "vtt" {
		data = VTTtoSRT(data)
	}
	if len(data) == 0 {
		return library.Entry{}, errors.New("subtitle contains no supported cues")
	}
	return a.saveAsset(id, "."+language+".srt", data)
}

type assetRecord struct {
	Suffix string `json:"suffix"`
	File   string `json:"file"`
	Size   int64  `json:"size"`
}

var assetID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func (a *App) loadAssets(id string) []library.Entry {
	if !assetID.MatchString(id) {
		return nil
	}
	dir := filepath.Join(a.original.DataDir, "assets")
	b, e := os.ReadFile(filepath.Join(dir, id+".json"))
	if e != nil || len(b) > 64<<10 {
		return nil
	}
	var records []assetRecord
	if json.Unmarshal(b, &records) != nil {
		return nil
	}
	out := []library.Entry{}
	for _, r := range records {
		if filepath.Base(r.File) != r.File || r.Size <= 0 || r.Size > 8<<20 || !strings.HasPrefix(r.Suffix, ".") || strings.ContainsAny(r.Suffix, "/\\") {
			return nil
		}
		file := filepath.Join(dir, r.File)
		if stat, e := os.Stat(file); e != nil || stat.Size() != r.Size {
			return nil
		}
		out = append(out, library.Entry{Path: r.Suffix, Media: localAsset{file: file, size: r.Size}})
	}
	return out
}

var (
	cueTime   = regexp.MustCompile(`^((?:\d{2}:)?\d{2}:\d{2}\.\d{3})\s+-->\s+((?:\d{2}:)?\d{2}:\d{2}\.\d{3})`)
	cueMarkup = regexp.MustCompile(`<[^>]*>`)
)

// VTTtoSRT converts bounded WebVTT cues to numbered SubRip text.
func VTTtoSRT(raw []byte) []byte {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	var out strings.Builder
	n := 0
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(block, "\n")
		for i, line := range lines {
			matches := cueTime.FindStringSubmatch(line)
			if len(matches) != 3 || i+1 >= len(lines) {
				continue
			}
			n++
			start, end := matches[1], matches[2]
			if len(start) == 9 {
				start = "00:" + start
			}
			if len(end) == 9 {
				end = "00:" + end
			}
			out.WriteString(strconv.Itoa(n) + "\n" + strings.ReplaceAll(start, ".", ",") + " --> " + strings.ReplaceAll(end, ".", ",") + "\n" + html.UnescapeString(cueMarkup.ReplaceAllString(strings.Join(lines[i+1:], "\n"), "")) + "\n\n")
			break
		}
	}
	return []byte(out.String())
}
