package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
)

func TestMuxBoundsTrackInterleaveAndPreservesPackets(t *testing.T) {
	if os.Getenv("DUPLEX_MEDIA_TEST") != "1" {
		t.Skip("requires controlled FFmpeg media fixtures")
	}

	dir := t.TempDir()
	videoPath, audioPath := fixture(t, dir)

	video, err := os.ReadFile(videoPath) //nolint:gosec // G304: Both source files are generated beneath t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	audio, err := os.ReadFile(audioPath) //nolint:gosec // G304: Both source files are generated beneath t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	virtual, err := Mux(t.Context(), indexTestSource{"video", video}, indexTestSource{"audio", audio})
	if err != nil {
		t.Fatal(err)
	}

	movie, err := readMoov(t.Context(), virtual)
	if err != nil {
		t.Fatal(err)
	}

	for _, track := range movie.Traks {
		table := track.Mdia.Minf.Stbl
		for i := range table.Co64.ChunkOffset {
			chunk, err := table.Stsc.GetChunk(uint32(i + 1))
			if err != nil {
				t.Fatal(err)
			}

			first, _, err := table.Stts.GetDecodeTime(chunk.StartSampleNr)
			if err != nil {
				t.Fatal(err)
			}

			last, duration, err := table.Stts.GetDecodeTime(chunk.StartSampleNr + chunk.NrSamples - 1)
			if err != nil {
				t.Fatal(err)
			}

			if chunk.NrSamples > 1 && last+uint64(duration)-first > uint64(track.Mdia.Mdhd.Timescale)/2 {
				t.Fatal("source chunk defeated half-second interleaving", track.Mdia.Hdlr.HandlerType, chunk, last+uint64(duration)-first, track.Mdia.Mdhd.Timescale)
			}
		}
	}

	output := filepath.Join(dir, "interleaved.mp4")

	file, err := os.Create(output) //nolint:gosec // G304: The controlled output is beneath t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	_, err = io.Copy(file, &Seeker{Context: t.Context(), Source: virtual})
	if err = errors.Join(err, file.Close()); err != nil {
		t.Fatal(err)
	}

	reference := filepath.Join(dir, "reference.mp4")
	run(t, "ffmpeg", "-v", "error", "-i", videoPath, "-i", audioPath, "-map", "0:v:0", "-map", "1:a:0", "-c", "copy", reference)

	if !reflect.DeepEqual(packetSignatures(t, output), packetSignatures(t, reference)) {
		t.Fatal("time-based chunking changed packet bytes or timestamps")
	}
}

func TestLegacyLayoutRetainsIdentityBytesAndSavedFormat(t *testing.T) {
	one, two := indexTestSource{"one", []byte("abc")}, indexTestSource{"two", []byte("def")}
	legacy := &VirtualMP4{Revision: "progressive-mp4-v1", Header: []byte("original-header!"), Sources: []string{"one", "two"}, Length: 22, Spans: []Span{{Output: 16, SourceID: "one", Length: 3, source: one}, {Output: 19, SourceID: "two", Length: 3, source: two}}}
	legacy.setIdentity()

	if legacy.ID() != "6575c5e57ef9dcf666193508bb3eac7437edcddcf2054f1b5c89ebde45bf13d6" {
		t.Fatal("legacy identity algorithm changed", legacy.ID())
	}

	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := legacy.SaveIndex(path); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(path) //nolint:gosec // G304: The controlled legacy index is beneath t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadIndex(path, map[string]Representation{"one": one, "two": two})
	if err != nil || loaded.ID() != legacy.ID() || loaded.Revision != "progressive-mp4-v1" {
		t.Fatal("legacy representation identity changed", loaded, err)
	}

	data := make([]byte, loaded.Size())
	if _, err = loaded.ReadAt(context.Background(), data, 0); err != nil || string(data) != "original-header!abcdef" {
		t.Fatal("legacy handle bytes changed", string(data), err)
	}

	if err = loaded.SaveIndex(path); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(path) //nolint:gosec // G304: The controlled legacy index is beneath t.TempDir.
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("legacy saved format was rewritten", err)
	}
}

func TestChunkPlanPreservesGapsVariableTimingAndWholeSamples(t *testing.T) {
	sizes := []uint32{9, 10, 11, 2 << 20, 13, 14, 15, 16, 17, 18, 19, 20}

	stsc := &mp4.StscBox{}
	if err := stsc.AddEntry(1, 7, 1); err != nil {
		t.Fatal(err)
	}

	if err := stsc.AddEntry(2, 5, 1); err != nil {
		t.Fatal(err)
	}

	firstBytes := uint64(0)
	for _, size := range sizes[:7] {
		firstBytes += uint64(size)
	}

	offsets := []uint64{16, 16 + firstBytes + 100}

	data := make([]byte, 3<<20)
	for i := range data {
		data[i] = byte(i % 251)
	}

	table := &mp4.StblBox{Stsc: stsc, Stsz: &mp4.StszBox{SampleSize: sizes}, Stts: &mp4.SttsBox{SampleCount: []uint32{3, 1, 8}, SampleTimeDelta: []uint32{100, 800, 75}}, Co64: &mp4.Co64Box{ChunkOffset: offsets}}
	table.Children = []mp4.Box{stsc, table.Stsz, table.Stts, table.Co64}
	track := muxTrack{source: indexTestSource{"track", data}, track: &mp4.TrakBox{Mdia: &mp4.MdiaBox{Mdhd: &mp4.MdhdBox{Timescale: 1000}, Minf: &mp4.MinfBox{Stbl: table}}}}

	spans, err := track.timedSpans(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}

	var (
		got           []byte
		samples       uint32
		previousEnd   uint64
		sawLongSample bool
	)

	for i, span := range spans {
		chunk, err := table.Stsc.GetChunk(uint32(i + 1))
		if err != nil || chunk.StartSampleNr != samples+1 || chunk.NrSamples != span.samples {
			t.Fatal("output sample-to-chunk mapping changed sample order", chunk, span, err)
		}

		last, duration, err := table.Stts.GetDecodeTime(samples + span.samples)
		if err != nil || span.dts != previousEnd {
			t.Fatal("chunk timing is discontinuous", span.dts, previousEnd, err)
		}

		previousEnd = last + uint64(duration)
		if span.samples > 1 && (previousEnd-span.dts > 500 || span.Length > ChunkSize) {
			t.Fatal("output chunk exceeded its bounds", span)
		}

		if span.Length > ChunkSize {
			sawLongSample = span.samples == 1
		}

		got = append(got, data[span.Offset:span.Offset+span.Length]...)
		samples += span.samples
	}

	want := append(bytes.Clone(data[16:16+firstBytes]), data[offsets[1]:offsets[1]+90]...)
	if !bytes.Equal(got, want) || samples != 12 || previousEnd != 1700 || !sawLongSample {
		t.Fatal("chunk splitting changed source bytes or sample timing", samples, previousEnd, sawLongSample, len(got), len(want))
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := track.timedSpans(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("chunk planning ignored cancellation", err)
	}

	for _, invalid := range []*mp4.SttsBox{
		{},
		{SampleCount: []uint32{12}},
		{SampleCount: []uint32{0}, SampleTimeDelta: []uint32{100}},
		{SampleCount: []uint32{11}, SampleTimeDelta: []uint32{100}},
	} {
		table.Stts = invalid
		if _, err := newMuxSamples(table); !errors.Is(err, ErrUnsupported) {
			t.Fatal("invalid timing counts were accepted", err)
		}
	}
}
