package media

import (
	"context"
	"fmt"
	"math"

	"github.com/Eyevinn/mp4ff/mp4"
)

type fragmentTrack struct {
	trak   *mp4.TrakBox
	trex   *mp4.TrexBox
	table  *mp4.StblBox
	time   uint64
	sample uint32
}

// normalizeFragments synthesizes sample tables without reading media payloads.
// Encryption, implicit cross-track addressing, and discontinuities fail closed.
func normalizeFragments(ctx context.Context, source Representation, movie *mp4.MoovBox) (*mp4.MoovBox, error) {
	tracks, err := fragmentTracks(movie)
	if err != nil {
		return nil, err
	}
	reader := &indexReader{ctx: ctx, source: source}
	fragments := 0
	for offset, count := int64(0), 0; offset < source.Size(); count++ {
		if count > 100000 {
			return nil, ErrUnsupported
		}
		box, err := reader.boxHeader(offset)
		if err != nil {
			return nil, err
		}
		switch box.kind {
		case "sidx":
			if err = reader.prefetchBox(ctx, box); err != nil {
				return nil, err
			}
		case "moof":
			if err = reader.appendMoof(box, tracks); err != nil {
				return nil, err
			}
			fragments++
		}
		offset += box.size
	}
	if fragments == 0 {
		return nil, fmt.Errorf("%w: no finite fragments", ErrUnsupported)
	}
	if err = finishFragmentTracks(movie, tracks); err != nil {
		return nil, err
	}
	return movie, nil
}

func fragmentTracks(movie *mp4.MoovBox) (map[uint32]*fragmentTrack, error) {
	if movie == nil || movie.Mvhd == nil || movie.Mvhd.Timescale == 0 || movie.Mvex == nil {
		return nil, ErrUnsupported
	}
	tracks := map[uint32]*fragmentTrack{}
	for _, track := range movie.Traks {
		if track.Tkhd == nil {
			return nil, ErrUnsupported
		}
		defaults, ok := movie.Mvex.GetTrex(track.Tkhd.TrackID)
		if !ok {
			return nil, ErrUnsupported
		}
		normalized, err := newFragmentTrack(track, defaults)
		if err != nil {
			return nil, err
		}
		if tracks[track.Tkhd.TrackID] != nil {
			return nil, ErrUnsupported
		}
		tracks[track.Tkhd.TrackID] = normalized
	}
	return tracks, nil
}

func newFragmentTrack(track *mp4.TrakBox, defaults *mp4.TrexBox) (*fragmentTrack, error) {
	if track.Mdia == nil || track.Mdia.Mdhd == nil || track.Mdia.Mdhd.Timescale == 0 || track.Mdia.Minf == nil || track.Mdia.Minf.Stbl == nil {
		return nil, ErrUnsupported
	}
	old := track.Mdia.Minf.Stbl
	if old.Stsd == nil || len(old.Stsd.Children) != 1 {
		return nil, ErrUnsupported
	}
	table := mp4.NewStblBox()
	table.AddChild(old.Stsd)
	for _, box := range []mp4.Box{&mp4.SttsBox{}, &mp4.StscBox{}, &mp4.StszBox{}, &mp4.Co64Box{}, &mp4.StssBox{}, &mp4.CttsBox{Version: 1, EndSampleNr: []uint32{0}}} {
		table.AddChild(box)
	}
	for n, box := range track.Mdia.Minf.Children {
		if box.Type() == "stbl" {
			track.Mdia.Minf.Children[n] = table
		}
	}
	track.Mdia.Minf.Stbl = table
	return &fragmentTrack{trak: track, trex: defaults, table: table}, nil
}

func (r *indexReader) prefetchBox(ctx context.Context, box sourceBox) error {
	if len(r.windows) > 0 {
		return nil
	}
	decoded, err := r.decode(box, maxIndexBytes)
	if err != nil {
		return err
	}
	index, ok := decoded.(*mp4.SidxBox)
	if !ok {
		return ErrUnsupported
	}
	return r.prefetch(ctx, index)
}

func (r *indexReader) appendMoof(box sourceBox, tracks map[uint32]*fragmentTrack) error {
	decoded, err := r.decode(box, 4<<20)
	if err != nil {
		return err
	}
	moof, ok := decoded.(*mp4.MoofBox)
	if !ok || len(moof.Trafs) == 0 {
		return ErrUnsupported
	}
	for _, traf := range moof.Trafs {
		if err = validTraf(traf); err != nil {
			return err
		}
		track := tracks[traf.Tfhd.TrackID]
		if track == nil {
			return ErrUnsupported
		}
		if err = track.appendTraf(traf, box.offset, r.source.Size(), len(moof.Trafs)); err != nil {
			return err
		}
	}
	return nil
}

func validTraf(traf *mp4.TrafBox) error {
	if traf == nil || traf.Tfhd == nil || traf.Tfdt == nil {
		return fmt.Errorf("%w: incomplete fragment", ErrUnsupported)
	}
	if traf.Senc != nil || traf.Saio != nil || traf.Saiz != nil || traf.UUIDSenc != nil || traf.Sbgp != nil || traf.Sgpd != nil {
		return fmt.Errorf("%w: encrypted or auxiliary fragment", ErrUnsupported)
	}
	return nil
}

func fragmentBase(header *mp4.TfhdBox, offset, size int64, tracks int) (int64, error) {
	if size < 1 || offset < 0 {
		return 0, ErrUnsupported
	}
	if header.HasBaseDataOffset() {
		if header.BaseDataOffset > math.MaxInt64 {
			return 0, ErrUnsupported
		}
		base := int64(header.BaseDataOffset)
		if base > size {
			return 0, ErrUnsupported
		}
		return base, nil
	}
	if !header.DefaultBaseIfMoof() && tracks > 1 {
		return 0, fmt.Errorf("%w: implicit multi-track fragment base", ErrUnsupported)
	}
	return offset, nil
}

func (f *fragmentTrack) appendTraf(traf *mp4.TrafBox, offset, size int64, tracks int) error {
	if traf.Tfdt.BaseMediaDecodeTime() != f.time {
		return fmt.Errorf("%w: discontinuous fragment timeline", ErrUnsupported)
	}
	if traf.Tfhd.HasSampleDescriptionIndex() && traf.Tfhd.SampleDescriptionIndex != 1 {
		return ErrUnsupported
	}
	base, err := fragmentBase(traf.Tfhd, offset, size, tracks)
	if err != nil {
		return err
	}
	cursor := int64(-1)
	for _, run := range traf.Truns {
		if run.HasDataOffset() {
			cursor, err = fragmentPosition(base, run.DataOffset, size)
			if err != nil {
				return err
			}
		}
		if cursor < 0 || len(run.Samples) == 0 || len(run.Samples) > 10_000_000 {
			return fmt.Errorf("%w: fragment has no valid explicit sample range", ErrUnsupported)
		}
		run.AddSampleDefaultValues(traf.Tfhd, f.trex)
		if err = f.addChunk(cursor, len(run.Samples)); err != nil {
			return err
		}
		for _, sample := range run.Samples {
			cursor, err = f.appendSample(sample, cursor, size)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func fragmentPosition(base int64, offset int32, size int64) (int64, error) {
	delta := int64(offset)
	if base < 0 || base > size || (delta > 0 && delta > size-base) || (delta < 0 && -delta > base) {
		return 0, ErrUnsupported
	}
	return base + delta, nil
}

func (f *fragmentTrack) addChunk(offset int64, samples int) error {
	if offset < 0 || samples < 1 || samples > math.MaxUint32 || len(f.table.Co64.ChunkOffset) >= math.MaxUint32 {
		return ErrUnsupported
	}
	f.table.Co64.ChunkOffset = append(f.table.Co64.ChunkOffset, uint64(offset))
	return f.table.Stsc.AddEntry(uint32(len(f.table.Co64.ChunkOffset)), uint32(samples), 1) //nolint:gosec // G115: Both counts are checked against MaxUint32 before the append.
}

func (f *fragmentTrack) appendSample(sample mp4.Sample, cursor, size int64) (int64, error) {
	if cursor < 0 || cursor > size || sample.Size == 0 || sample.Dur == 0 || int64(sample.Size) > size-cursor || f.sample >= 10_000_000 {
		return 0, ErrUnsupported
	}
	limit := uint64(f.trak.Mdia.Mdhd.Timescale) * 31 * 86400
	if f.time > limit || uint64(sample.Dur) > limit-f.time {
		return 0, ErrUnsupported
	}
	f.table.Stsz.SampleSize = append(f.table.Stsz.SampleSize, sample.Size)
	f.sample++
	f.time += uint64(sample.Dur)
	f.appendTiming(sample)
	// YouTube may leave sample_depends_on unspecified while explicitly marking
	// non-sync samples. Requiring depends_on=2 would discard valid seek points.
	if fragmentSync(sample.Flags) {
		f.table.Stss.SampleNumber = append(f.table.Stss.SampleNumber, f.sample)
	}
	return cursor + int64(sample.Size), nil
}

func (f *fragmentTrack) appendTiming(sample mp4.Sample) {
	table := f.table
	n := len(table.Stts.SampleCount) - 1
	if n >= 0 && table.Stts.SampleTimeDelta[n] == sample.Dur {
		table.Stts.SampleCount[n]++
	} else {
		table.Stts.SampleCount = append(table.Stts.SampleCount, 1)
		table.Stts.SampleTimeDelta = append(table.Stts.SampleTimeDelta, sample.Dur)
	}
	n = len(table.Ctts.SampleOffset) - 1
	if n >= 0 && table.Ctts.SampleOffset[n] == sample.CompositionTimeOffset {
		table.Ctts.EndSampleNr[n+1] = f.sample
	} else {
		table.Ctts.SampleOffset = append(table.Ctts.SampleOffset, sample.CompositionTimeOffset)
		table.Ctts.EndSampleNr = append(table.Ctts.EndSampleNr, f.sample)
	}
}

func finishFragmentTracks(movie *mp4.MoovBox, tracks map[uint32]*fragmentTrack) error {
	for _, track := range tracks {
		if track.sample == 0 {
			return ErrUnsupported
		}
		track.table.Stsz.SampleNumber = track.sample
		track.trak.Mdia.Mdhd.Duration = track.time
		track.trak.Mdia.Mdhd.Version = 1
		track.trak.Tkhd.Duration = rescale(track.time, track.trak.Mdia.Mdhd.Timescale, movie.Mvhd.Timescale)
		track.trak.Tkhd.Version = 1
		movie.Mvhd.Duration = max(movie.Mvhd.Duration, track.trak.Tkhd.Duration)
	}
	return nil
}
func fragmentSync(flags uint32) bool { return flags&0x10000 == 0 && flags&0x03000000 != 0x01000000 }
