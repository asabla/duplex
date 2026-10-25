package artwork

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

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
