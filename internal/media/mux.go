package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"sort"

	"github.com/Eyevinn/mp4ff/mp4"
)

// PackagingVersion identifies the immutable progressive MP4 layout.
const (
	PackagingVersion = "progressive-mp4-v1"
	maxIndexBytes    = 64 * 1024 * 1024
)

// Span maps an output range to source bytes. Headers are generated separately;
// payload bytes are never remuxed by a timestamp-dependent subprocess.
type Span struct {
	Output       int64  `json:"output"`
	SourceID     string `json:"source_id"`
	Offset       int64  `json:"offset"`
	Length       int64  `json:"length"`
	source       Representation
	track, chunk int
	dts          uint64
	scale        uint32
}

// VirtualMP4 maps deterministic progressive MP4 offsets to immutable source ranges.
type VirtualMP4 struct {
	Revision string   `json:"revision"`
	Header   []byte   `json:"header"`
	Spans    []Span   `json:"spans"`
	Sources  []string `json:"sources"`
	Info     Details  `json:"media"`
	Length   int64    `json:"length"`
	identity string
}

// ID returns the packaging revision and source-derived representation identity.
func (v *VirtualMP4) ID() string { return v.identity }

// Size returns the exact generated-header and mapped-payload length.
func (v *VirtualMP4) Size() int64 { return v.Length }

// Details returns the verified codec, dimensions, and duration.
func (v *VirtualMP4) Details() Details { return v.Info }

// ReadAt copies generated headers or exact upstream spans without preparing a full file.
func (v *VirtualMP4) ReadAt(ctx context.Context, p []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("negative offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if offset >= v.Length {
		return 0, io.EOF
	}
	wanted := len(p)
	p = p[:min(int64(len(p)), v.Length-offset)]
	n := 0
	if offset < int64(len(v.Header)) {
		n = copy(p, v.Header[offset:])
		offset += int64(n)
	}
	i := sort.Search(len(v.Spans), func(i int) bool { return v.Spans[i].Output+v.Spans[i].Length > offset })
	for n < len(p) && i < len(v.Spans) {
		span := v.Spans[i]
		delta := offset - span.Output
		if delta < 0 || delta >= span.Length {
			return n, errors.New("invalid virtual range map")
		}
		length := min(int64(len(p)-n), span.Length-delta)
		count, err := span.source.ReadAt(ctx, p[n:n+int(length)], span.Offset+delta)
		n += count
		offset += int64(count)
		if err != nil {
			return n, err
		}
		if count != int(length) {
			return n, io.ErrUnexpectedEOF
		}
		i++
	}
	if n < wanted {
		return n, io.EOF
	}
	return n, nil
}

// readMoov reads bounded metadata and skips media bodies. Fragmented sources
// are normalized to progressive sample tables before offset mapping.
func readMoov(ctx context.Context, s Representation) (result *mp4.MoovBox, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = fmt.Errorf("%w: malformed MP4 index", ErrUnsupported)
		}
	}()
	index := &indexReader{ctx: ctx, source: s}
	for offset, count := int64(0), 0; offset < s.Size() && count < 4096; count++ {
		box, err := index.boxHeader(offset)
		if err != nil {
			return nil, err
		}
		if box.kind == "moof" {
			return nil, fmt.Errorf("%w: fragmented source without an initialization index", ErrUnsupported)
		}
		if box.kind == "moov" {
			decoded, err := index.decode(box, maxIndexBytes)
			if err != nil {
				return nil, err
			}
			movie, ok := decoded.(*mp4.MoovBox)
			if !ok || movie.Mvhd == nil || movie.Mvhd.Timescale == 0 {
				return nil, fmt.Errorf("%w: invalid movie header", ErrUnsupported)
			}
			if movie.Mvex != nil {
				return normalizeFragments(ctx, s, movie)
			}
			return movie, nil
		}
		offset += box.size
	}
	return nil, fmt.Errorf("%w: missing movie index", ErrUnsupported)
}

// Mux combines one video track and one audio track from progressive MP4
// sources. It builds interleaved chunk offsets without reading media payloads.
// Inputs are decoded into private box trees and are not mutated themselves.
func Mux(ctx context.Context, video, audio Representation) (result *VirtualMP4, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = fmt.Errorf("%w: malformed sample tables", ErrUnsupported)
		}
	}()
	inputs := []Representation{video, audio}
	tracks := make([]muxTrack, 2)
	virtual := &VirtualMP4{Revision: PackagingVersion}
	for i, source := range inputs {
		track, err := inspectMuxTrack(ctx, source, []string{"vide", "soun"}[i])
		if err != nil {
			return nil, err
		}
		spans, err := track.sourceSpans(i)
		if err != nil {
			return nil, err
		}
		virtual.Spans = append(virtual.Spans, spans...)
		track.useOutputOffsets(len(spans))
		track.track.Tkhd.TrackID = uint32(i + 1)
		tracks[i] = track
	}
	// Rational decode timestamps are compared with full 128-bit products.
	sort.SliceStable(virtual.Spans, func(i, j int) bool {
		left, right := virtual.Spans[i], virtual.Spans[j]
		ah, al := bits.Mul64(left.dts, uint64(right.scale))
		bh, bl := bits.Mul64(right.dts, uint64(left.scale))
		return ah < bh || (ah == bh && al < bl)
	})
	movie, err := muxMovie(tracks)
	if err != nil {
		return nil, err
	}
	if err = virtual.buildHeader(movie, tracks); err != nil {
		return nil, err
	}
	videoTrack, audioTrack := tracks[0].track, tracks[1].track
	virtual.Sources = []string{video.ID(), audio.ID()}
	virtual.Info = Details{Container: "mp4", VideoCodec: videoTrack.Mdia.Minf.Stbl.Stsd.Children[0].Type(), AudioCodec: audioTrack.Mdia.Minf.Stbl.Stsd.Children[0].Type(), Width: int(videoTrack.Tkhd.Width >> 16), Height: int(videoTrack.Tkhd.Height >> 16), DurationSeconds: float64(videoTrack.Mdia.Mdhd.Duration) / float64(videoTrack.Mdia.Mdhd.Timescale)}
	virtual.setIdentity()
	return virtual, nil
}

func (v *VirtualMP4) setIdentity() {
	h := sha256.New()
	h.Write([]byte(v.Revision))
	for _, id := range v.Sources {
		h.Write([]byte(id))
	}
	h.Write(v.Header)
	v.identity = hex.EncodeToString(h.Sum(nil))
}

func rescale(v uint64, from, to uint32) uint64 {
	return (v/uint64(from))*uint64(to) + (v%uint64(from))*uint64(to)/uint64(from)
}
