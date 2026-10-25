package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
)

// Index reads use small windows instead of filling a 1 MiB media chunk for each
// fragment header. Media bodies are skipped. This state lasts only for a build.
type indexReader struct {
	ctx     context.Context
	source  Representation
	data    []byte
	offset  int64
	total   int64
	windows []indexWindow
}

type indexWindow struct {
	offset int64
	data   []byte
}

func (r *indexReader) read(p []byte, offset int64) error {
	if offset < 0 || int64(len(p)) > r.source.Size()-offset {
		return ErrUnsupported
	}
	if offset >= r.offset && offset+int64(len(p)) <= r.offset+int64(len(r.data)) {
		copy(p, r.data[offset-r.offset:])
		return nil
	}
	n := sort.Search(len(r.windows), func(n int) bool { return r.windows[n].offset+int64(len(r.windows[n].data)) > offset })
	if n < len(r.windows) {
		w := r.windows[n]
		if offset >= w.offset && offset+int64(len(p)) <= w.offset+int64(len(w.data)) {
			copy(p, w.data[offset-w.offset:])
			return nil
		}
	}
	length := min(max(len(p), 16*1024), int(r.source.Size()-offset))
	if r.total+int64(length) > maxIndexBytes {
		return fmt.Errorf("%w: index traffic exceeds 64 MiB", ErrUnsupported)
	}
	r.data = make([]byte, length)
	r.offset = offset
	r.total += int64(length)
	var err error
	if source, ok := r.source.(*HTTPSource); ok {
		r.data, err = source.fetch(r.ctx, offset, length)
	} else {
		_, err = r.source.ReadAt(r.ctx, r.data, offset)
	}
	if err != nil {
		return err
	}
	copy(p, r.data)
	return nil
}

// A flat sidx provides independent fragment offsets. Fetch bounded header
// windows with eight workers, then parse in source order. This avoids a serial
// round trip for every fragment in a multi-hour replay.
func (r *indexReader) prefetch(sidx *mp4.SidxBox) error {
	source, ok := r.source.(*HTTPSource)
	if !ok || len(r.windows) > 0 {
		return nil
	}
	if len(sidx.SidxRefs) == 0 || sidx.AnchorPoint > uint64(source.Size()) {
		return ErrUnsupported
	}
	pos := int64(sidx.AnchorPoint)
	windows := make([]indexWindow, 0, len(sidx.SidxRefs))
	var required int64
	for _, ref := range sidx.SidxRefs {
		if ref.ReferenceType != 0 {
			return nil
		}
		size := int64(ref.ReferencedSize)
		if size < 8 || size > source.Size()-pos {
			return ErrUnsupported
		}
		length := min(size, int64(4096))
		if required+length > maxIndexBytes-r.total {
			return fmt.Errorf("%w: fragment header budget exceeds 64 MiB", ErrUnsupported)
		}
		required += length
		windows = append(windows, indexWindow{offset: pos, data: make([]byte, length)})
		pos += size
	}
	if required > maxIndexBytes-r.total {
		return fmt.Errorf("%w: fragment header budget exceeds 64 MiB", ErrUnsupported)
	}
	r.total += required
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	jobs := make(chan int)
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	for range min(8, len(windows)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				w := &windows[n]
				data, err := source.fetch(ctx, w.offset, len(w.data))
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					cancel()
					return
				}
				w.data = data
			}
		}()
	}
	go func() {
		defer close(jobs)
		// A warm CDN can answer tiny headers faster than the origin permits
		// follow-up requests. Bound request rate as well as worker count.
		ticker := time.NewTicker(40 * time.Millisecond)
		defer ticker.Stop()
		for n := range windows {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
			select {
			case jobs <- n:
			case <-ctx.Done():
				return
			}
		}
	}()
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	r.windows = windows
	return nil
}

type fragmentTrack struct {
	trak   *mp4.TrakBox
	trex   *mp4.TrexBox
	table  *mp4.StblBox
	time   uint64
	sample uint32
}

// normalizeFragments builds progressive sample tables from finite fragmented
// MP4 metadata. It does not copy sample payloads. Discontinuities, encryption,
// implicit cross-track addressing and sample-description changes fail closed.
func normalizeFragments(ctx context.Context, source Representation, m *mp4.MoovBox) (*mp4.MoovBox, error) {
	tracks := map[uint32]*fragmentTrack{}
	for _, t := range m.Traks {
		if t.Tkhd == nil || t.Mdia == nil || t.Mdia.Mdhd == nil || t.Mdia.Minf == nil || t.Mdia.Minf.Stbl == nil {
			return nil, ErrUnsupported
		}
		trex, ok := m.Mvex.GetTrex(t.Tkhd.TrackID)
		if !ok {
			return nil, ErrUnsupported
		}
		old := t.Mdia.Minf.Stbl
		if old.Stsd == nil || len(old.Stsd.Children) != 1 {
			return nil, ErrUnsupported
		}
		st := mp4.NewStblBox()
		st.AddChild(old.Stsd)
		st.AddChild(&mp4.SttsBox{})
		st.AddChild(&mp4.StscBox{})
		st.AddChild(&mp4.StszBox{})
		st.AddChild(&mp4.Co64Box{})
		st.AddChild(&mp4.StssBox{})
		st.AddChild(&mp4.CttsBox{Version: 1, EndSampleNr: []uint32{0}})
		for n, b := range t.Mdia.Minf.Children {
			if b.Type() == "stbl" {
				t.Mdia.Minf.Children[n] = st
			}
		}
		t.Mdia.Minf.Stbl = st
		tracks[t.Tkhd.TrackID] = &fragmentTrack{trak: t, trex: trex, table: st}
	}
	r := &indexReader{ctx: ctx, source: source}
	fragments := 0
	for pos, boxes := int64(0), 0; pos < source.Size(); boxes++ {
		if boxes > 100000 {
			return nil, ErrUnsupported
		}
		h := make([]byte, 16)
		if err := r.read(h[:8], pos); err != nil {
			return nil, err
		}
		size := int64(binary.BigEndian.Uint32(h))
		kind := string(h[4:8])
		header := int64(8)
		if size == 1 {
			if err := r.read(h[8:], pos+8); err != nil {
				return nil, err
			}
			u := binary.BigEndian.Uint64(h[8:])
			if u > uint64(source.Size()) {
				return nil, ErrUnsupported
			}
			size = int64(u)
			header = 16
		}
		if size == 0 {
			size = source.Size() - pos
		}
		if size < header || size > source.Size()-pos {
			return nil, ErrUnsupported
		}
		if kind == "sidx" && len(r.windows) == 0 {
			if size > maxIndexBytes {
				return nil, ErrUnsupported
			}
			data := make([]byte, size)
			if err := r.read(data, pos); err != nil {
				return nil, err
			}
			box, err := mp4.DecodeBox(uint64(pos), bytes.NewReader(data))
			if err != nil {
				return nil, ErrUnsupported
			}
			sidx, ok := box.(*mp4.SidxBox)
			if !ok {
				return nil, ErrUnsupported
			}
			if err = r.prefetch(sidx); err != nil {
				return nil, err
			}
		}
		if kind == "moof" {
			if size > 4*1024*1024 {
				return nil, ErrUnsupported
			}
			data := make([]byte, size)
			if err := r.read(data, pos); err != nil {
				return nil, err
			}
			box, err := mp4.DecodeBox(uint64(pos), bytes.NewReader(data))
			if err != nil {
				return nil, fmt.Errorf("%w: invalid fragment", ErrUnsupported)
			}
			moof, ok := box.(*mp4.MoofBox)
			if !ok {
				return nil, ErrUnsupported
			}
			for _, traf := range moof.Trafs {
				if traf.Tfhd == nil || traf.Tfdt == nil || traf.Senc != nil || traf.Saio != nil || traf.Saiz != nil || traf.UUIDSenc != nil || traf.Sbgp != nil || traf.Sgpd != nil {
					return nil, fmt.Errorf("%w: encrypted or incomplete fragment", ErrUnsupported)
				}
				f := tracks[traf.Tfhd.TrackID]
				if f == nil {
					return nil, ErrUnsupported
				}
				if traf.Tfdt.BaseMediaDecodeTime() != f.time {
					return nil, fmt.Errorf("%w: discontinuous fragment timeline", ErrUnsupported)
				}
				if traf.Tfhd.HasSampleDescriptionIndex() && traf.Tfhd.SampleDescriptionIndex != 1 {
					return nil, ErrUnsupported
				}
				base := pos
				if traf.Tfhd.HasBaseDataOffset() {
					if traf.Tfhd.BaseDataOffset > uint64(source.Size()) {
						return nil, ErrUnsupported
					}
					base = int64(traf.Tfhd.BaseDataOffset)
				} else if !traf.Tfhd.DefaultBaseIfMoof() && len(moof.Trafs) > 1 {
					return nil, fmt.Errorf("%w: implicit multi-track fragment base", ErrUnsupported)
				}
				cursor := int64(-1)
				for _, trun := range traf.Truns {
					if trun.HasDataOffset() {
						cursor = base + int64(trun.DataOffset)
					}
					if cursor < 0 || len(trun.Samples) == 0 {
						return nil, fmt.Errorf("%w: fragment has no explicit sample range", ErrUnsupported)
					}
					trun.AddSampleDefaultValues(traf.Tfhd, f.trex)
					st := f.table
					st.Co64.ChunkOffset = append(st.Co64.ChunkOffset, uint64(cursor))
					if err = st.Stsc.AddEntry(uint32(len(st.Co64.ChunkOffset)), uint32(len(trun.Samples)), 1); err != nil {
						return nil, err
					}
					for _, sample := range trun.Samples {
						if sample.Size == 0 || sample.Dur == 0 || int64(sample.Size) > source.Size()-cursor || f.sample >= 10_000_000 {
							return nil, ErrUnsupported
						}
						st.Stsz.SampleSize = append(st.Stsz.SampleSize, sample.Size)
						f.sample++
						cursor += int64(sample.Size)
						f.time += uint64(sample.Dur)
						if f.time > uint64(f.trak.Mdia.Mdhd.Timescale)*31*86400 {
							return nil, ErrUnsupported
						}
						n := len(st.Stts.SampleCount) - 1
						if n >= 0 && st.Stts.SampleTimeDelta[n] == sample.Dur {
							st.Stts.SampleCount[n]++
						} else {
							st.Stts.SampleCount = append(st.Stts.SampleCount, 1)
							st.Stts.SampleTimeDelta = append(st.Stts.SampleTimeDelta, sample.Dur)
						}
						// sample_depends_on may be unspecified (0). YouTube
						// still marks non-sync samples explicitly. mp4ff's
						// IsSync helper requires depends_on=2 and would lose
						// these valid random-access points.
						if fragmentSync(sample.Flags) {
							st.Stss.SampleNumber = append(st.Stss.SampleNumber, f.sample)
						}
						n = len(st.Ctts.SampleOffset) - 1
						if n >= 0 && st.Ctts.SampleOffset[n] == sample.CompositionTimeOffset {
							st.Ctts.EndSampleNr[n+1] = f.sample
						} else {
							st.Ctts.SampleOffset = append(st.Ctts.SampleOffset, sample.CompositionTimeOffset)
							st.Ctts.EndSampleNr = append(st.Ctts.EndSampleNr, f.sample)
						}
					}
				}
			}
			fragments++
		}
		pos += size
	}
	if fragments == 0 {
		return nil, fmt.Errorf("%w: no finite fragments", ErrUnsupported)
	}
	for _, f := range tracks {
		if f.sample == 0 {
			return nil, ErrUnsupported
		}
		f.table.Stsz.SampleNumber = f.sample
		f.trak.Mdia.Mdhd.Duration = f.time
		f.trak.Mdia.Mdhd.Version = 1
		f.trak.Tkhd.Duration = rescale(f.time, f.trak.Mdia.Mdhd.Timescale, m.Mvhd.Timescale)
		f.trak.Tkhd.Version = 1
		m.Mvhd.Duration = max(m.Mvhd.Duration, f.trak.Tkhd.Duration)
	}
	return m, nil
}

func fragmentSync(flags uint32) bool { return flags&0x10000 == 0 && flags&0x03000000 != 0x01000000 }
