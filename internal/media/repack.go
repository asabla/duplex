package media

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/Eyevinn/mp4ff/mp4"
)

// Repack builds the current layout using only a checked legacy header and map.
// It does not fetch payload, change the input map, or infer a new source version.
func Repack(ctx context.Context, old *VirtualMP4) (result *VirtualMP4, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = fmt.Errorf("%w: malformed legacy layout", ErrUnsupported)
		}
	}()

	if old == nil || old.Revision != legacyPackagingVersion {
		return nil, ErrUnsupported
	}

	if err = old.validateLayout(); err != nil {
		return nil, err
	}

	header := &VirtualMP4{Header: old.Header, Length: int64(len(old.Header))}

	movie, err := readMoov(ctx, header)
	if err != nil {
		return nil, err
	}

	if len(movie.Traks) != 2 {
		return nil, ErrUnsupported
	}

	tracks := make([]muxTrack, 2)

	for i, kind := range []string{"vide", "soun"} {
		track, err := legacyMuxTrack(old, movie, kind)
		if err != nil {
			return nil, err
		}

		tracks[i] = track
	}

	result, err = muxTracks(ctx, tracks)
	if err != nil {
		return nil, err
	}

	// Detached source sizes come only from the retained index. Older formats
	// without exact sizes still require live verification on their first miss.
	result.SourceLengths = old.sourceLengths()
	for i := range result.Spans {
		result.Spans[i].source = nil
	}

	if err = result.validateLayout(); err != nil {
		return nil, err
	}

	return result, nil
}

type layoutSource struct {
	id   string
	size int64
}

func (s layoutSource) ID() string                                         { return s.id }
func (s layoutSource) Size() int64                                        { return s.size }
func (s layoutSource) ReadAt(context.Context, []byte, int64) (int, error) { return 0, ErrUnsupported }

func legacyMuxTrack(old *VirtualMP4, movie *mp4.MoovBox, kind string) (muxTrack, error) {
	var selected *mp4.TrakBox

	for _, track := range movie.Traks {
		if track.Mdia != nil && track.Mdia.Hdlr != nil && track.Mdia.Hdlr.HandlerType == kind {
			if selected != nil {
				return muxTrack{}, ErrUnsupported
			}

			selected = track
		}
	}

	if selected == nil {
		return muxTrack{}, ErrUnsupported
	}

	if err := validateMuxTrack(selected, kind); err != nil {
		return muxTrack{}, err
	}

	table := selected.Mdia.Minf.Stbl
	if table.Co64 == nil {
		return muxTrack{}, ErrUnsupported
	}

	id, size, err := restoreSourceOffsets(old, table)
	if err != nil {
		return muxTrack{}, err
	}

	return muxTrack{movie: movie, track: selected, source: layoutSource{id: id, size: size}}, nil
}

func restoreSourceOffsets(old *VirtualMP4, table *mp4.StblBox) (string, int64, error) {
	id, size := "", int64(0)

	for i, offset := range table.Co64.ChunkOffset {
		if offset > math.MaxInt64 {
			return "", 0, ErrUnsupported
		}

		target := int64(offset)

		position := sort.Search(len(old.Spans), func(n int) bool { return old.Spans[n].Output >= target })
		if position == len(old.Spans) || old.Spans[position].Output != target {
			return "", 0, ErrUnsupported
		}

		span := old.Spans[position]
		if span.Offset < 0 {
			return "", 0, ErrUnsupported
		}

		if id != "" && id != span.SourceID {
			return "", 0, ErrUnsupported
		}

		chunk, err := table.Stsc.GetChunk(uint32(i + 1))
		if err != nil {
			return "", 0, err
		}

		length, err := table.Stsz.GetTotalSampleSize(chunk.StartSampleNr, chunk.StartSampleNr+chunk.NrSamples-1)
		if err != nil || length > math.MaxInt64 || int64(length) != span.Length {
			return "", 0, ErrUnsupported
		}

		id = span.SourceID
		size = max(size, span.Offset+span.Length)
		table.Co64.ChunkOffset[i] = uint64(span.Offset)
	}

	if id == "" {
		return "", 0, ErrUnsupported
	}

	return id, max(size, old.SourceLengths[id]), nil
}
