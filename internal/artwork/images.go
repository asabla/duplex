package artwork

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // PNG is one of the two supported input formats.
	"io"

	_ "golang.org/x/image/webp" // YouTube can return WebP branding; uploads remain JPEG/PNG only.
)

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

func encode(picture image.Image) ([]byte, error) {
	var data bytes.Buffer
	err := jpeg.Encode(&data, picture, &jpeg.Options{Quality: 90})
	return data.Bytes(), err
}
