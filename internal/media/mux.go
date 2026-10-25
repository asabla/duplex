package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"sort"

	"github.com/Eyevinn/mp4ff/mp4"
)

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

type VirtualMP4 struct {
	Revision string   `json:"revision"`
	Header   []byte   `json:"header"`
	Spans    []Span   `json:"spans"`
	Sources  []string `json:"sources"`
	Info     Details  `json:"media"`
	Length   int64    `json:"length"`
	identity string
}

func (v *VirtualMP4) ID() string       { return v.identity }
func (v *VirtualMP4) Size() int64      { return v.Length }
func (v *VirtualMP4) Details() Details { return v.Info }

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

// readMoov reads bounded metadata and skips media bodies. Fragmented sources are
// rejected until their separate index implementation has passed the same gate.
func readMoov(ctx context.Context, s Representation) (result *mp4.MoovBox, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = fmt.Errorf("%w: malformed MP4 index", ErrUnsupported)
		}
	}()
	index := &indexReader{ctx: ctx, source: s}
	for pos, boxes := int64(0), 0; pos < s.Size() && boxes < 4096; boxes++ {
		h := make([]byte, 16)
		if err = index.read(h[:8], pos); err != nil {
			return nil, err
		}
		size := int64(binary.BigEndian.Uint32(h))
		kind := string(h[4:8])
		header := int64(8)
		if size == 1 {
			if err = index.read(h[8:], pos+8); err != nil {
				return nil, err
			}
			u := binary.BigEndian.Uint64(h[8:])
			if u > uint64(s.Size()) {
				return nil, ErrUnsupported
			}
			size = int64(u)
			header = 16
		}
		if size == 0 {
			size = s.Size() - pos
		}
		if size < header || size > s.Size()-pos {
			return nil, fmt.Errorf("%w: invalid box bounds", ErrUnsupported)
		}
		if kind == "moof" {
			return nil, fmt.Errorf("%w: fragmented source", ErrUnsupported)
		}
		if kind == "moov" {
			if size > maxIndexBytes {
				return nil, fmt.Errorf("%w: index exceeds 64 MiB", ErrUnsupported)
			}
			data := make([]byte, size)
			if err = index.read(data, pos); err != nil {
				return nil, err
			}
			box, er := mp4.DecodeBox(uint64(pos), bytes.NewReader(data))
			if er != nil {
				return nil, fmt.Errorf("%w: invalid moov", ErrUnsupported)
			}
			moov, ok := box.(*mp4.MoovBox)
			if !ok || moov.Mvhd == nil || moov.Mvhd.Timescale == 0 {
				return nil, fmt.Errorf("%w: invalid movie header", ErrUnsupported)
			}
			if moov.Mvex != nil {
				return normalizeFragments(ctx, s, moov)
			}
			return moov, nil
		}
		pos += size
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
	tracks := make([]*mp4.TrakBox, 2)
	movies := make([]*mp4.MoovBox, 2)
	v := &VirtualMP4{Revision: PackagingVersion}
	for i, s := range inputs {
		if s == nil {
			return nil, errors.New("two source tracks are required")
		}
		m, err := readMoov(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("%s index: %w", []string{"video", "audio"}[i], err)
		}
		movies[i] = m
		kind := []string{"vide", "soun"}[i]
		for _, t := range m.Traks {
			if t.Mdia != nil && t.Mdia.Hdlr != nil && t.Mdia.Hdlr.HandlerType == kind {
				if tracks[i] != nil {
					return nil, ErrUnsupported
				}
				tracks[i] = t
			}
		}
		if tracks[i] == nil {
			return nil, fmt.Errorf("%w: missing %s track", ErrUnsupported, kind)
		}
		t := tracks[i]
		if t.Tkhd == nil || t.Mdia.Mdhd == nil || t.Mdia.Mdhd.Timescale == 0 || t.Mdia.Minf == nil || t.Mdia.Minf.Stbl == nil {
			return nil, ErrUnsupported
		}
		st := t.Mdia.Minf.Stbl
		if st.Stsd == nil || len(st.Stsd.Children) != 1 || st.Stsc == nil || st.Stsz == nil || st.Stts == nil || st.Saio != nil || st.Saiz != nil {
			return nil, fmt.Errorf("%w: incomplete or auxiliary sample tables", ErrUnsupported)
		}
		codec := st.Stsd.Children[0].Type()
		if (i == 0 && codec != "avc1" && codec != "avc3" && codec != "av01" && codec != "vp09") || (i == 1 && codec != "mp4a" && codec != "Opus") {
			return nil, fmt.Errorf("%w: codec %s", ErrUnsupported, codec)
		}
		offsets := []uint64{}
		if st.Stco != nil {
			for _, o := range st.Stco.ChunkOffset {
				offsets = append(offsets, uint64(o))
			}
		} else if st.Co64 != nil {
			offsets = append(offsets, st.Co64.ChunkOffset...)
		} else {
			return nil, ErrUnsupported
		}
		if len(offsets) == 0 || len(st.Stsc.Entries) == 0 || st.Stsc.Entries[0].FirstChunk != 1 || st.Stsz.GetNrSamples() > 10_000_000 {
			return nil, fmt.Errorf("%w: missing or oversized chunk index", ErrUnsupported)
		}
		sample := uint32(1)
		entry := 0
		for c, o := range offsets {
			for entry+1 < len(st.Stsc.Entries) && uint32(c+1) >= st.Stsc.Entries[entry+1].FirstChunk {
				entry++
			}
			count := st.Stsc.Entries[entry].SamplesPerChunk
			if count == 0 || sample > st.Stsz.GetNrSamples() || count > st.Stsz.GetNrSamples()-sample+1 {
				return nil, ErrUnsupported
			}
			length, err := st.Stsz.GetTotalSampleSize(sample, sample+count-1)
			if err != nil {
				return nil, err
			}
			dts, _, err := st.Stts.GetDecodeTime(sample)
			if err != nil {
				return nil, err
			}
			if o > uint64(s.Size()) || length > uint64(s.Size())-o || dts > uint64(t.Mdia.Mdhd.Timescale)*31*86400 {
				return nil, ErrUnsupported
			}
			v.Spans = append(v.Spans, Span{SourceID: s.ID(), Offset: int64(o), Length: int64(length), source: s, track: i, chunk: c, dts: dts, scale: t.Mdia.Mdhd.Timescale})
			sample += count
		}
		if sample != st.Stsz.GetNrSamples()+1 {
			return nil, ErrUnsupported
		}
		// Always use 64-bit chunk offsets, so output size cannot change when
		// crossing 4 GiB. Remove the old table from the encoded box tree.
		children := st.Children[:0]
		for _, b := range st.Children {
			if b.Type() != "stco" && b.Type() != "co64" {
				children = append(children, b)
			}
		}
		st.Children = children
		st.Stco = nil
		st.Co64 = nil
		st.AddChild(&mp4.Co64Box{ChunkOffset: make([]uint64, len(offsets))})
		t.Tkhd.TrackID = uint32(i + 1)
	}
	// Compare rational timestamps using full 128-bit products.
	sort.SliceStable(v.Spans, func(i, j int) bool {
		ah, al := bits.Mul64(v.Spans[i].dts, uint64(v.Spans[j].scale))
		bh, bl := bits.Mul64(v.Spans[j].dts, uint64(v.Spans[i].scale))
		return ah < bh || (ah == bh && al < bl)
	})
	moov := mp4.NewMoovBox()
	mvhd := movies[0].Mvhd
	mvhd.Version = 1
	mvhd.NextTrackID = 3
	moov.AddChild(mvhd)
	for i, t := range tracks {
		scale := movies[i].Mvhd.Timescale
		if t.Tkhd.Duration > uint64(scale)*31*86400 {
			return nil, ErrUnsupported
		}
		t.Tkhd.Version = 1
		t.Tkhd.Duration = rescale(t.Tkhd.Duration, scale, mvhd.Timescale)
		if t.Edts != nil {
			for _, elst := range t.Edts.Elst {
				elst.Version = 1
				for n := range elst.Entries {
					e := &elst.Entries[n]
					if e.SegmentDuration > uint64(scale)*31*86400 {
						return nil, ErrUnsupported
					}
					e.SegmentDuration = rescale(e.SegmentDuration, scale, mvhd.Timescale)
				}
			}
		}
		mvhd.Duration = max(mvhd.Duration, t.Tkhd.Duration)
		moov.AddChild(t)
	}
	var header bytes.Buffer
	// Stable compatible brands. Codec details remain in each sample entry.
	ftyp := []byte{0, 0, 0, 28, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0, 'i', 's', 'o', 'm', 'i', 's', 'o', '6', 'm', 'p', '4', '1'}
	base := int64(len(ftyp)) + int64(moov.Size()) + 16
	pos := base
	for n := range v.Spans {
		s := &v.Spans[n]
		s.Output = pos
		tracks[s.track].Mdia.Minf.Stbl.Co64.ChunkOffset[s.chunk] = uint64(pos)
		if s.Length > int64(^uint64(0)>>1)-pos {
			return nil, ErrUnsupported
		}
		pos += s.Length
	}
	header.Write(ftyp)
	if err := moov.Encode(&header); err != nil {
		return nil, err
	}
	mdat := make([]byte, 16)
	binary.BigEndian.PutUint32(mdat, 1)
	copy(mdat[4:8], "mdat")
	binary.BigEndian.PutUint64(mdat[8:], uint64(pos-base+16))
	header.Write(mdat)
	if int64(header.Len()) != base {
		return nil, errors.New("unstable MP4 header size")
	}
	v.Header = header.Bytes()
	v.Length = pos
	v.Sources = []string{video.ID(), audio.ID()}
	v.Info = Details{Container: "mp4", VideoCodec: tracks[0].Mdia.Minf.Stbl.Stsd.Children[0].Type(), AudioCodec: tracks[1].Mdia.Minf.Stbl.Stsd.Children[0].Type(), Width: int(tracks[0].Tkhd.Width >> 16), Height: int(tracks[0].Tkhd.Height >> 16), DurationSeconds: float64(tracks[0].Mdia.Mdhd.Duration) / float64(tracks[0].Mdia.Mdhd.Timescale)}
	v.setIdentity()
	return v, nil
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
