package media

import (
	"context"

	"github.com/Eyevinn/mp4ff/mp4"
)

const maxMuxChunks = 1_000_000

// muxSamples walks timing runs once. Repeated GetDecodeTime searches per output
// chunk would make variable-rate tracks quadratic in their sample-table size.
type muxSamples struct {
	table   *mp4.StblBox
	sample  uint32
	run     int
	left    uint32
	decoded uint64
}

func newMuxSamples(table *mp4.StblBox) (muxSamples, error) {
	times := table.Stts
	if len(times.SampleCount) == 0 || len(times.SampleCount) != len(times.SampleTimeDelta) {
		return muxSamples{}, ErrUnsupported
	}

	var total uint64

	for _, count := range times.SampleCount {
		if count == 0 {
			return muxSamples{}, ErrUnsupported
		}

		total += uint64(count)
	}

	if total != uint64(table.Stsz.GetNrSamples()) {
		return muxSamples{}, ErrUnsupported
	}

	return muxSamples{table: table, sample: 1, left: times.SampleCount[0]}, nil
}

// A single long or large sample stays intact. Every other output chunk fits
// half a second and one cache chunk, regardless of source chunk duration.
func (s *muxSamples) nextChunk(ctx context.Context, remaining, scale uint32) (uint32, int64, error) {
	start := s.decoded
	count, length := uint32(0), int64(0)

	for count < remaining {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}

		duration := uint64(s.table.Stts.SampleTimeDelta[s.run])

		size := int64(s.table.Stsz.GetSampleSize(int(s.sample)))
		if count > 0 && (s.decoded-start+duration > max(uint64(1), uint64(scale)/2) || length+size > ChunkSize) {
			break
		}

		count++
		length += size
		s.sample++
		s.decoded += duration
		s.left--

		if s.left == 0 && s.run+1 < len(s.table.Stts.SampleCount) {
			s.run++
			s.left = s.table.Stts.SampleCount[s.run]
		}
	}

	if length == 0 || s.decoded > uint64(scale)*31*86400 {
		return 0, 0, ErrUnsupported
	}

	return count, length, nil
}

type muxChunkPlan struct {
	spans []Span
	table mp4.StscBox
	last  uint32
}

func (p *muxChunkPlan) append(span Span, samples uint32) error {
	if len(p.spans) >= maxMuxChunks {
		return ErrUnsupported
	}

	number, err := chunkNumber(len(p.spans) + 1)
	if err != nil {
		return err
	}

	if len(p.spans) == 0 || p.last != samples {
		if err = p.table.AddEntry(number, samples, 1); err != nil {
			return err
		}
	}

	span.chunk, span.samples = len(p.spans), samples
	p.spans = append(p.spans, span)
	p.last = samples

	return nil
}

func (t muxTrack) timedSpans(ctx context.Context, track int) ([]Span, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	input, err := t.sourceSpans(track)
	if err != nil {
		return nil, err
	}

	samples, err := newMuxSamples(t.track.Mdia.Minf.Stbl)
	if err != nil {
		return nil, err
	}

	plan := muxChunkPlan{}

	for i, original := range input {
		if t.track.Mdia.Minf.Stbl.Stsc.GetSampleDescriptionID(i+1) != 1 {
			return nil, ErrUnsupported
		}

		for remaining := original.samples; remaining > 0; {
			span := original
			span.dts = samples.decoded

			count, length, err := samples.nextChunk(ctx, remaining, original.scale)
			if err != nil {
				return nil, err
			}

			span.Length = length
			if err = plan.append(span, count); err != nil {
				return nil, err
			}

			original.Offset += length
			remaining -= count
		}
	}

	t.replaceChunkTable(&plan.table)

	return plan.spans, nil
}

func (t muxTrack) replaceChunkTable(chunks *mp4.StscBox) {
	table := t.track.Mdia.Minf.Stbl
	for i, child := range table.Children {
		if child.Type() == "stsc" {
			table.Children[i] = chunks
		}
	}

	table.Stsc = chunks
}
