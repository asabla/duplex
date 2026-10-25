package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"duplex/internal/cache"
)

func TestCombinedMP4IsInspectedWithoutFullPreparation(t *testing.T) {
	if os.Getenv("DUPLEX_MEDIA_TEST") != "1" {
		t.Skip("requires controlled FFmpeg fixtures")
	}
	dir := t.TempDir()
	video, audio := fixture(t, dir)
	combined := filepath.Join(dir, "combined.mp4")
	fragmented := filepath.Join(dir, "fragmented.mp4")
	run(t, "ffmpeg", "-v", "error", "-i", video, "-i", audio, "-map", "0:v:0", "-map", "1:a:0", "-c", "copy", "-movflags", "+faststart", combined)
	run(t, "ffmpeg", "-v", "error", "-i", combined, "-c", "copy", "-movflags", "empty_moov+frag_keyframe+default_base_moof", fragmented)
	server := sourceServer(t, combined, fragmented, video, audio)
	store, err := cache.New(filepath.Join(dir, "chunks"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, store)
	client := &http.Client{Timeout: 5 * time.Second}
	upstream, err := OpenHTTP(t.Context(), client, server.URL+"/combined.mp4", "combined", store)
	if err != nil {
		t.Fatal(err)
	}
	info, err := InspectCombined(t.Context(), upstream)
	if err != nil || info.VideoCodec != "avc1" || info.AudioCodec != "mp4a" || info.Width != 640 || info.Height != 360 || info.DurationSeconds != 30 {
		t.Fatal(info, err)
	}
	if upstream.UpstreamBytes.Load() >= upstream.Size()/2 {
		t.Fatal("inspection prepared media payload", upstream.UpstreamBytes.Load(), upstream.Size())
	}
	described := Described{Representation: upstream, Info: info}
	if described.Details() != info {
		t.Fatal("verified media details lost")
	}
	if err = VerifyRanges(t.Context(), described); err != nil {
		t.Fatal(err)
	}
	if upstream.UpstreamBytes.Load() >= upstream.Size() {
		t.Fatal("range verification fetched the full object")
	}
	for _, filename := range []string{"fragmented.mp4", "video.mp4", "audio.m4a"} {
		invalid, openErr := OpenHTTP(t.Context(), client, server.URL+"/"+filename, filename, store)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, err = InspectCombined(t.Context(), invalid); !errors.Is(err, ErrUnsupported) {
			t.Fatal("combined profile accepted an unsupported container", filename, err)
		}
	}
}

type rangeContractFixture struct {
	data  []byte
	call  int
	fault string
}

func (r *rangeContractFixture) ID() string  { return "controlled-range" }
func (r *rangeContractFixture) Size() int64 { return int64(len(r.data)) }
func (r *rangeContractFixture) ReadAt(ctx context.Context, p []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.call++
	if offset == r.Size() && r.fault == "invalid-end" {
		return 0, nil
	}
	if r.fault == "first-error" && r.call == 1 || r.fault == "repeat-error" && r.call == 2 {
		return 0, ErrExpired
	}
	if r.fault == "first-short" && r.call == 1 || r.fault == "repeat-short" && r.call == 2 {
		return 0, nil
	}
	n, err := bytes.NewReader(r.data).ReadAt(p, offset)
	if r.fault == "changed" && r.call == 2 && n > 0 {
		p[0] ^= 1
	}
	return n, err
}

func TestRangeContractRejectsShortChangedExpiredAndInvalidEndReads(t *testing.T) {
	for _, tc := range []struct {
		fault string
		want  error
	}{
		{"", nil}, {"first-error", ErrExpired}, {"repeat-error", ErrExpired}, {"first-short", io.ErrUnexpectedEOF}, {"repeat-short", io.ErrUnexpectedEOF}, {"changed", ErrChanged}, {"invalid-end", ErrUnsupported},
	} {
		rep := &rangeContractFixture{data: bytes.Repeat([]byte("range"), 40<<10), fault: tc.fault}
		if err := VerifyRanges(t.Context(), rep); !errors.Is(err, tc.want) {
			t.Fatal(tc.fault, err)
		}
	}
	if err := VerifyRanges(t.Context(), &rangeContractFixture{}); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := VerifyRanges(ctx, &rangeContractFixture{data: []byte("cancel")}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestBoxHeadersRejectImpossibleSizesBeforeAllocation(t *testing.T) {
	for _, tc := range []struct {
		ordinary        uint32
		large           uint64
		extended, valid bool
	}{
		{ordinary: 8, valid: true},
		{valid: true},
		{ordinary: 7},
		{ordinary: 17},
		{large: 16, extended: true, valid: true},
		{large: 15, extended: true},
		{large: 1 << 63, extended: true},
	} {
		data := make([]byte, 16)
		copy(data[4:8], "free")
		if tc.extended {
			binary.BigEndian.PutUint32(data, 1)
			binary.BigEndian.PutUint64(data[8:], tc.large)
		} else {
			binary.BigEndian.PutUint32(data, tc.ordinary)
		}
		reader := indexReader{ctx: t.Context(), source: memorySource{data}}
		_, err := reader.boxHeader(0)
		if (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
	}
	reader := indexReader{ctx: t.Context(), source: memorySource{make([]byte, 16)}}
	for _, offset := range []int64{-1, 17} {
		if _, err := reader.boxHeader(offset); !errors.Is(err, ErrUnsupported) {
			t.Fatal(offset, err)
		}
	}
}
