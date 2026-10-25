// Package artwork renders deterministic posters and validates durable uploads.
package artwork

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // PNG is one of the two supported input formats.
	"io"
	"os"
	"path/filepath"
	"strings"

	"duplex/internal/config"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp" // YouTube can return WebP branding; uploads remain JPEG/PNG only.
)

// Poster dimensions and upload limits apply to automatic and manual artwork.
const (
	Width     = 1000
	Height    = 1500
	MaxUpload = 10 << 20
	MaxPixels = 24_000_000
)

// ErrImage reports an invalid format, pixel count, or upload length.
var ErrImage = errors.New("artwork must be a JPEG or PNG up to 10 MiB and 24 megapixels")

// Input distinguishes channel branding from a collection's own landscape image.
type Input struct {
	Channel, Title, Label   string
	Avatar, Banner, Picture image.Image
}

// Decode validates dimensions and format before allocating decoded pixels.
func Decode(data []byte) (image.Image, error) { return decodeImage(data, false) }

// DecodeSource also accepts WebP branding while retaining the same allocation limits.
func DecodeSource(data []byte) (image.Image, error) { return decodeImage(data, true) }

func decodeImage(data []byte, source bool) (image.Image, error) {
	if len(data) == 0 || len(data) > MaxUpload {
		return nil, ErrImage
	}
	info, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png" && (!source || format != "webp")) || info.Width < 1 || info.Height < 1 || int64(info.Width)*int64(info.Height) > MaxPixels {
		return nil, ErrImage
	}
	result, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrImage
	}
	return result, nil
}

// Upload strips source metadata and places the complete image on a 2:3 canvas.
func Upload(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxUpload+1))
	if err != nil {
		return nil, err
	}
	picture, err := Decode(data)
	if err != nil {
		return nil, err
	}
	canvas := image.NewRGBA(image.Rect(0, 0, Width, Height))
	fill(canvas, canvas.Bounds(), color.RGBA{20, 24, 30, 255})
	contain(canvas, canvas.Bounds(), picture)
	return encode(canvas)
}

// Render uses the embedded Go fonts, licensed by Bigelow & Holmes under BSD-3.
// Images retain their aspect ratios. Titles wrap to a bounded text area.
func Render(input Input) ([]byte, error) {
	canvas := image.NewRGBA(image.Rect(0, 0, Width, Height))
	fill(canvas, canvas.Bounds(), color.RGBA{20, 24, 30, 255})
	seed := sha256.Sum256([]byte(input.Channel))
	headerColor := color.RGBA{32 + seed[0]/4, 32 + seed[1]/4, 32 + seed[2]/4, 255}
	if input.Avatar != nil || input.Banner != nil || input.Picture != nil {
		headerColor = color.RGBA{27, 32, 39, 255}
	}
	fill(canvas, image.Rect(0, 0, Width, 580), headerColor)
	picture := input.Picture
	if picture == nil {
		picture = input.Banner
	}
	if picture != nil {
		contain(canvas, image.Rect(0, 0, Width, 580), picture)
	}
	if input.Avatar != nil && picture == nil {
		contain(canvas, image.Rect(180, 40, 820, 680), input.Avatar)
	} else if input.Avatar != nil {
		fill(canvas, image.Rect(48, 464, 296, 712), color.RGBA{240, 242, 245, 255})
		contain(canvas, image.Rect(60, 476, 284, 700), input.Avatar)
	}
	identity := input.Channel
	if input.Label == "CHANNEL" {
		identity = "YOUTUBE CHANNEL"
	}
	if err := text(canvas, identity, image.Pt(56, 785), 40, 2, false); err != nil {
		return nil, err
	}
	if err := text(canvas, input.Title, image.Pt(56, 920), 68, 4, true); err != nil {
		return nil, err
	}
	fill(canvas, image.Rect(56, 1340, 944, 1343), color.RGBA{115, 125, 138, 255})
	if err := text(canvas, input.Label, image.Pt(56, 1412), 32, 1, false); err != nil {
		return nil, err
	}
	return encode(canvas)
}

func fill(dst draw.Image, area image.Rectangle, c color.Color) {
	draw.Draw(dst, area, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

func contain(dst draw.Image, box image.Rectangle, src image.Image) {
	b := src.Bounds()
	if b.Empty() {
		return
	}
	width, height := box.Dx(), box.Dy()
	if int64(b.Dx())*int64(height) > int64(b.Dy())*int64(width) {
		height = max(1, int(int64(b.Dy())*int64(width)/int64(b.Dx())))
	} else {
		width = max(1, int(int64(b.Dx())*int64(height)/int64(b.Dy())))
	}
	start := image.Pt(box.Min.X+(box.Dx()-width)/2, box.Min.Y+(box.Dy()-height)/2)
	xdraw.CatmullRom.Scale(dst, image.Rectangle{Min: start, Max: start.Add(image.Pt(width, height))}, src, b, draw.Over, nil)
}

func text(dst draw.Image, value string, point image.Point, size float64, lines int, bold bool) (err error) {
	data := goregular.TTF
	if bold {
		data = gobold.TTF
	}
	parsed, err := opentype.Parse(data)
	if err != nil {
		return err
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, face.Close()) }()
	drawer := font.Drawer{Dst: dst, Src: image.NewUniform(color.RGBA{242, 244, 247, 255}), Face: face}
	parts := wrap(&drawer, strings.Join(strings.Fields(value), " "), Width-112, lines)
	for i, line := range parts {
		drawer.Dot = fixed.P(point.X, point.Y+i*int(size*1.25))
		drawer.DrawString(line)
	}
	return nil
}

func wrap(d *font.Drawer, value string, width, lines int) []string {
	out := []string{}
	line := ""
	for _, r := range value {
		if d.MeasureString(line+string(r)).Ceil() > width && line != "" {
			out = append(out, strings.TrimSpace(line))
			line = ""
			if len(out) == lines {
				last := []rune(out[len(out)-1])
				if len(last) > 2 {
					out[len(out)-1] = string(last[:len(last)-2]) + "…"
				}
				return out
			}
		}
		line += string(r)
	}
	if line != "" && len(out) < lines {
		out = append(out, strings.TrimSpace(line))
	}
	return out
}

func encode(picture image.Image) ([]byte, error) {
	var data bytes.Buffer
	err := jpeg.Encode(&data, picture, &jpeg.Options{Quality: 90})
	return data.Bytes(), err
}

// Save stores only normalized, content-addressed assets outside the media cache.
func Save(dir string, data []byte) (string, error) {
	info, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" || info.Width != Width || info.Height != Height {
		return "", ErrImage
	}
	hash := sha256.Sum256(data)
	ref := hex.EncodeToString(hash[:]) + ".jpg"
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err = config.AtomicWrite(filepath.Join(dir, ref), data, 0o600); err != nil {
		return "", err
	}
	return ref, nil
}

// Path validates a YAML reference before constructing a local asset path.
func Path(dir, reference string) (string, error) {
	if !config.ValidArtworkReference(reference) {
		return "", fmt.Errorf("invalid artwork reference")
	}
	return filepath.Join(dir, reference), nil
}

// Fingerprint includes the rendered labels and image pixels. It lets callers
// keep the previous poster when fetching returns identical inputs.
func Fingerprint(input Input) (string, error) {
	hash := sha256.New()
	for _, value := range []string{input.Channel, input.Title, input.Label} {
		if _, err := fmt.Fprintf(hash, "%d:%s", len(value), value); err != nil {
			return "", err
		}
	}
	for _, picture := range []image.Image{input.Avatar, input.Banner, input.Picture} {
		if picture == nil {
			if _, err := hash.Write([]byte{0}); err != nil {
				return "", err
			}
			continue
		}
		if _, err := hash.Write([]byte{1}); err != nil {
			return "", err
		}
		if err := jpeg.Encode(hash, picture, &jpeg.Options{Quality: 90}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
