package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/Eyevinn/mp4ff/mp4"
)

type muxTrack struct {
	movie  *mp4.MoovBox
	track  *mp4.TrakBox
	source Representation
}

func inspectMuxTrack(ctx context.Context, source Representation, kind string) (muxTrack, error) {
	result := muxTrack{source: source}
	if source == nil || source.Size() < 1 {
		return result, errors.New("two finite source tracks are required")
	}
	movie, err := readMoov(ctx, source)
	if err != nil {
		return result, fmt.Errorf("%s index: %w", kind, err)
	}
	result.movie = movie
	for _, track := range movie.Traks {
		if track.Mdia == nil || track.Mdia.Hdlr == nil || track.Mdia.Hdlr.HandlerType != kind {
			continue
		}
		if result.track != nil {
			return result, ErrUnsupported
		}
		result.track = track
	}
	if result.track == nil {
		return result, fmt.Errorf("%w: missing %s track", ErrUnsupported, kind)
	}
	if err = validateMuxTrack(result.track, kind); err != nil {
		return result, err
	}
	return result, nil
}

func validateMuxTrack(track *mp4.TrakBox, kind string) error {
	if track.Tkhd == nil || track.Mdia.Mdhd == nil || track.Mdia.Mdhd.Timescale == 0 || track.Mdia.Minf == nil || track.Mdia.Minf.Stbl == nil {
		return ErrUnsupported
	}
	table := track.Mdia.Minf.Stbl
	if table.Stsd == nil || len(table.Stsd.Children) != 1 || table.Stsc == nil || table.Stsz == nil || table.Stts == nil || table.Saio != nil || table.Saiz != nil {
		return fmt.Errorf("%w: incomplete or auxiliary sample tables", ErrUnsupported)
	}
	codec := table.Stsd.Children[0].Type()
	if (kind == "vide" && !videoCodec(codec)) || (kind == "soun" && !audioCodec(codec)) {
		return fmt.Errorf("%w: codec %s", ErrUnsupported, codec)
	}
	if track.Mdia.Mdhd.Duration == 0 || track.Mdia.Mdhd.Duration > uint64(track.Mdia.Mdhd.Timescale)*31*86400 {
		return ErrUnsupported
	}
	return nil
}

func sourceOffsets(table *mp4.StblBox) ([]uint64, error) {
	switch {
	case table.Stco != nil:
		result := make([]uint64, len(table.Stco.ChunkOffset))
		for i, offset := range table.Stco.ChunkOffset {
			result[i] = uint64(offset)
		}
		return result, nil
	case table.Co64 != nil:
		return table.Co64.ChunkOffset, nil
	default:
		return nil, ErrUnsupported
	}
}

func (t muxTrack) sourceSpans(track int) ([]Span, error) {
	table := t.track.Mdia.Minf.Stbl
	offsets, err := sourceOffsets(table)
	if err != nil {
		return nil, err
	}
	if len(offsets) == 0 || len(table.Stsc.Entries) == 0 || table.Stsc.Entries[0].FirstChunk != 1 || table.Stsz.GetNrSamples() > 10_000_000 {
		return nil, fmt.Errorf("%w: missing or oversized chunk index", ErrUnsupported)
	}
	result := make([]Span, 0, len(offsets))
	sample, entry := uint32(1), 0
	for chunk, offset := range offsets {
		number, err := chunkNumber(chunk + 1)
		if err != nil {
			return nil, err
		}
		for entry+1 < len(table.Stsc.Entries) && number >= table.Stsc.Entries[entry+1].FirstChunk {
			entry++
		}
		count := table.Stsc.Entries[entry].SamplesPerChunk
		if count == 0 || sample > table.Stsz.GetNrSamples() || count > table.Stsz.GetNrSamples()-sample+1 {
			return nil, ErrUnsupported
		}
		span, err := t.sourceSpan(offset, sample, count)
		if err != nil {
			return nil, err
		}
		span.track, span.chunk = track, chunk
		result = append(result, span)
		sample += count
	}
	if sample != table.Stsz.GetNrSamples()+1 {
		return nil, ErrUnsupported
	}
	return result, nil
}

func chunkNumber(n int) (uint32, error) {
	if n < 1 || n > math.MaxUint32 {
		return 0, ErrUnsupported
	}
	return uint32(n), nil
}

func (t muxTrack) sourceSpan(offset uint64, sample, count uint32) (Span, error) {
	table := t.track.Mdia.Minf.Stbl
	length, err := table.Stsz.GetTotalSampleSize(sample, sample+count-1)
	if err != nil {
		return Span{}, err
	}
	dts, _, err := table.Stts.GetDecodeTime(sample)
	if err != nil {
		return Span{}, err
	}
	size := t.source.Size()
	if size < 1 || offset > math.MaxInt64 || length > math.MaxInt64 {
		return Span{}, ErrUnsupported
	}
	start, bytes := int64(offset), int64(length)
	if start > size || bytes > size-start || dts > uint64(t.track.Mdia.Mdhd.Timescale)*31*86400 {
		return Span{}, ErrUnsupported
	}
	return Span{SourceID: t.source.ID(), Offset: start, Length: bytes, source: t.source, dts: dts, scale: t.track.Mdia.Mdhd.Timescale}, nil
}

func (t muxTrack) useOutputOffsets(count int) {
	// Fixed-width offsets make header size stable when output crosses 4 GiB.
	table := t.track.Mdia.Minf.Stbl
	children := table.Children[:0]
	for _, box := range table.Children {
		if box.Type() != "stco" && box.Type() != "co64" {
			children = append(children, box)
		}
	}
	table.Children = children
	table.Stco = nil
	table.Co64 = nil
	table.AddChild(&mp4.Co64Box{ChunkOffset: make([]uint64, count)})
}

func muxMovie(tracks []muxTrack) (*mp4.MoovBox, error) {
	movie := mp4.NewMoovBox()
	header := tracks[0].movie.Mvhd
	header.Version = 1
	header.NextTrackID = 3
	movie.AddChild(header)
	for _, track := range tracks {
		if err := alignMovieTimeline(track, header.Timescale); err != nil {
			return nil, err
		}
		header.Duration = max(header.Duration, track.track.Tkhd.Duration)
		movie.AddChild(track.track)
	}
	return movie, nil
}

func alignMovieTimeline(t muxTrack, to uint32) error {
	from := t.movie.Mvhd.Timescale
	track := t.track
	if track.Tkhd.Duration > uint64(from)*31*86400 {
		return ErrUnsupported
	}
	track.Tkhd.Version = 1
	track.Tkhd.Duration = rescale(track.Tkhd.Duration, from, to)
	if track.Edts == nil {
		return nil
	}
	for _, list := range track.Edts.Elst {
		list.Version = 1
		for n := range list.Entries {
			entry := &list.Entries[n]
			if entry.SegmentDuration > uint64(from)*31*86400 {
				return ErrUnsupported
			}
			entry.SegmentDuration = rescale(entry.SegmentDuration, from, to)
		}
	}
	return nil
}

func (v *VirtualMP4) buildHeader(movie *mp4.MoovBox, tracks []muxTrack) error {
	brands := []byte{0, 0, 0, 28, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0, 'i', 's', 'o', 'm', 'i', 's', 'o', '6', 'm', 'p', '4', '1'}
	size := movie.Size()
	if size > uint64(maxIndexBytes) {
		return ErrUnsupported
	}
	base := int64(len(brands)) + int64(size) + 16
	end, err := v.assignOffsets(base, tracks)
	if err != nil {
		return err
	}
	var header bytes.Buffer
	header.Write(brands)
	if err = movie.Encode(&header); err != nil {
		return err
	}
	payload := make([]byte, 16)
	binary.BigEndian.PutUint32(payload, 1)
	copy(payload[4:8], "mdat")
	// assignOffsets guarantees end >= base > 16, so this sum cannot overflow.
	binary.BigEndian.PutUint64(payload[8:], uint64(end-base+16))
	header.Write(payload)
	if int64(header.Len()) != base {
		return errors.New("unstable MP4 header size")
	}
	v.Header = header.Bytes()
	v.Length = end
	return nil
}

func (v *VirtualMP4) assignOffsets(base int64, tracks []muxTrack) (int64, error) {
	if base < 16 {
		return 0, ErrUnsupported
	}
	position := base
	for n := range v.Spans {
		span := &v.Spans[n]
		if span.Length < 0 || span.Length > math.MaxInt64-position {
			return 0, ErrUnsupported
		}
		span.Output = position
		tracks[span.track].track.Mdia.Minf.Stbl.Co64.ChunkOffset[span.chunk] = uint64(position)
		position += span.Length
	}
	return position, nil
}
