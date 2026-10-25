package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"duplex/internal/config"
	"duplex/internal/library"
	"duplex/internal/source"
)

func httpRequest(ctx context.Context, raw string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
}
func (a *App) acquireAssets(ctx context.Context, m source.Metadata, c source.Candidate) []library.Entry {
	out := []library.Entry{}
	defer func() {
		records := []assetRecord{}
		for _, entry := range out {
			if file, ok := entry.Media.(localAsset); ok {
				records = append(records, assetRecord{Suffix: entry.Path, File: filepath.Base(file.file), Size: file.size})
			}
		}
		b, _ := json.Marshal(records)
		_ = config.AtomicWrite(filepath.Join(a.original.DataDir, "assets", m.ID+".json"), b, 0600)
	}()
	var picture image.Image
	if data, e := fetchAsset(ctx, m.Thumbnail); e == nil {
		if info, _, e := image.DecodeConfig(bytes.NewReader(data)); e == nil && info.Width > 0 && info.Height > 0 && int64(info.Width)*int64(info.Height) < 24_000_000 {
			picture, _, _ = image.Decode(bytes.NewReader(data))
		}
	}
	if picture == nil {
		h := sha256.Sum256([]byte(m.ChannelID))
		img := image.NewRGBA(image.Rect(0, 0, 480, 270))
		draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{h[0]/2 + 32, h[1]/2 + 32, h[2]/2 + 32, 255}}, image.Point{}, draw.Src)
		picture = img
	}
	var b bytes.Buffer
	if jpeg.Encode(&b, picture, &jpeg.Options{Quality: 85}) == nil {
		if entry, e := a.saveAsset(m.ID, ".jpg", b.Bytes()); e == nil {
			out = append(out, entry)
		}
	}
	language := m.Language
	if c.Audio != nil && c.Audio.Language != "" {
		language = c.Audio.Language
	}
	if language == "" {
		language = c.Video.Language
	}
	if language == "" {
		for code := range m.AutomaticCaptions {
			if strings.HasSuffix(code, "-orig") {
				language = strings.TrimSuffix(code, "-orig")
				break
			}
		}
	}
	if language == "" {
		return out
	}
	validLang := regexp.MustCompile(`^[A-Za-z0-9-]{1,25}$`)
	if !validLang.MatchString(language) {
		return out
	}
	tracks := m.Subtitles[language]
	if len(tracks) == 0 {
		for key, items := range m.Subtitles {
			if strings.Split(key, "-")[0] == strings.Split(language, "-")[0] {
				tracks = items
				break
			}
		}
	}
	if len(tracks) == 0 {
		tracks = m.AutomaticCaptions[language+"-orig"]
		if len(tracks) == 0 {
			tracks = m.AutomaticCaptions[language]
		}
	}
	for _, ext := range []string{"srt", "vtt"} {
		for _, track := range tracks {
			if track.Ext != ext {
				continue
			}
			data, e := fetchAsset(ctx, track.URL)
			if e != nil {
				continue
			}
			if ext == "vtt" {
				data = VTTtoSRT(data)
			}
			if len(data) == 0 {
				continue
			}
			if entry, e := a.saveAsset(m.ID, "."+language+".srt", data); e == nil {
				out = append(out, entry)
				return out
			}
		}
	}
	return out
}

type assetRecord struct {
	Suffix string `json:"suffix"`
	File   string `json:"file"`
	Size   int64  `json:"size"`
}

func (a *App) loadAssets(id string) []library.Entry {
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

var cueTime = regexp.MustCompile(`^((?:\d{2}:)?\d{2}:\d{2}\.\d{3})\s+-->\s+((?:\d{2}:)?\d{2}:\d{2}\.\d{3})`)
var cueMarkup = regexp.MustCompile(`<[^>]*>`)

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
