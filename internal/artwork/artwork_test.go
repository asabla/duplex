package artwork

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func picture(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fill(img, img.Bounds(), color.RGBA{210, 80, 40, 255})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestPosterCompositionAndDeterminism(t *testing.T) {
	input := Input{Channel: "Test Channel", Title: "A playlist with a sufficiently long title that must wrap across several lines", Label: "PLAYLIST"}
	img, err := Decode(picture(t, 320, 180))
	if err != nil {
		t.Fatal(err)
	}
	input.Banner = img
	input.Avatar = img
	first, err := Render(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(input)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("poster is not deterministic", err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(first))
	if err != nil || format != "jpeg" || config.Width != Width || config.Height != Height {
		t.Fatal(config, format, err)
	}
	key, err := Fingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Title = "Renamed"
	changed, err := Fingerprint(input)
	if err != nil || key == changed {
		t.Fatal("title omitted from inputs", err)
	}
	input.Banner = nil
	another, err := Fingerprint(input)
	if err != nil || changed == another {
		t.Fatal("image omitted from inputs", err)
	}
	fallback, err := Render(Input{Channel: "Missing imagery", Title: "2025", Label: "YEAR"})
	if err != nil || len(fallback) < 10000 {
		t.Fatal("labelled fallback not rendered", err)
	}
}

func TestContainPreservesImageProportions(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 400, 100))
	fill(src, src.Bounds(), color.White)
	dst := image.NewRGBA(image.Rect(0, 0, 200, 300))
	contain(dst, dst.Bounds(), src)
	if dst.RGBAAt(100, 124).A != 0 || dst.RGBAAt(100, 125).A != 255 || dst.RGBAAt(100, 174).A != 255 || dst.RGBAAt(100, 175).A != 0 {
		t.Fatal("landscape proportions changed")
	}
	contain(dst, dst.Bounds(), image.NewRGBA(image.Rect(0, 0, 0, 0)))
}

func TestUploadValidationAndContentAddressing(t *testing.T) {
	data, err := Upload(bytes.NewReader(picture(t, 400, 100)))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil || decoded.Bounds().Dx() != Width || decoded.Bounds().Dy() != Height {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ref, err := Save(dir, data)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Save(dir, data)
	if err != nil || again != ref {
		t.Fatal("not content addressed", err)
	}
	file, err := Path(dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(info, err)
	}
	if _, err = Path(dir, "../outside.jpg"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err = Save(dir, picture(t, 1, 1)); !errors.Is(err, ErrImage) {
		t.Fatal("unnormalized image accepted", err)
	}
	for _, bad := range [][]byte{nil, []byte("not an image"), data[:40], bytes.Repeat([]byte{0}, MaxUpload+1)} {
		if _, err = Upload(bytes.NewReader(bad)); !errors.Is(err, ErrImage) {
			t.Fatal("bad upload accepted", err)
		}
	}
	oversized := picture(t, 1, 1)
	binary.BigEndian.PutUint32(oversized[16:20], 24_000_001)
	binary.BigEndian.PutUint32(oversized[20:24], 1)
	binary.BigEndian.PutUint32(oversized[29:33], crc32.ChecksumIEEE(oversized[12:29]))
	if _, err = Decode(oversized); !errors.Is(err, ErrImage) {
		t.Fatal("pixel limit not enforced before decoding", err)
	}
	if _, err = Save(filepath.Join(dir, "not-directory", "assets"), data); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(dir, "file")
	if err = os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = Save(blocked, data); err == nil {
		t.Fatal("filesystem error hidden")
	}
	if _, err = Upload(errorReader{}); err == nil {
		t.Fatal("read error hidden")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
