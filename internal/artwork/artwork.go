// Package artwork renders deterministic posters and validates durable uploads.
package artwork

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"

	xdraw "golang.org/x/image/draw"
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
