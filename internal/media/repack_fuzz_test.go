package media

import (
	"bytes"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
)

func legacyFuzzMovie(t testing.TB) (*VirtualMP4, *mp4.MoovBox) {
	t.Helper()

	old, err := decodeLayout(bytes.NewReader(fuzzLayoutEnvelope(legacyLayoutSeed)))
	if err != nil {
		t.Fatal(err)
	}

	header := &VirtualMP4{Header: old.Header, Length: int64(len(old.Header))}

	movie, err := readMoov(t.Context(), header)
	if err != nil || len(movie.Traks) != 2 {
		t.Fatal("invalid retained track seed", err)
	}

	return old, movie
}

func FuzzRepackTables(f *testing.F) {
	_, movie := legacyFuzzMovie(f)
	table := movie.Traks[0].Mdia.Minf.Stbl

	for field, value := range []uint64{uint64(table.Stts.SampleCount[0]), uint64(table.Stts.SampleTimeDelta[0]), table.Co64.ChunkOffset[0], uint64(table.Stsz.SampleSize[0]), uint64(movie.Traks[0].Mdia.Mdhd.Timescale)} {
		f.Add(uint8(field), value)
		f.Add(uint8(field), uint64(0))
		f.Add(uint8(field), ^uint64(0))
	}

	f.Add(uint8(5), uint64(8996))

	f.Fuzz(func(t *testing.T, field uint8, value uint64) {
		old, movie := legacyFuzzMovie(t)
		track := movie.Traks[0]
		table := track.Mdia.Minf.Stbl
		bounded := uint32(value & 0xffffffff)

		switch field % 6 {
		case 0:
			table.Stts.SampleCount[0] = bounded
		case 1:
			table.Stts.SampleTimeDelta[0] = bounded
		case 2:
			table.Co64.ChunkOffset[0] = value
		case 3:
			table.Stsz.SampleSize[0] = bounded
		case 4:
			track.Mdia.Mdhd.Timescale = bounded
		case 5:
			// Keep the chunk size and address consistent with the audio span.
			// This reaches source ownership checks beyond individual table bounds.
			offset := old.Spans[1].Output
			if offset < 0 {
				t.Fatal("invalid retained chunk offset")
				return
			}

			remaining := len(table.Stsz.SampleSize) - 1
			if remaining < 0 || remaining > math.MaxUint32 {
				t.Fatal("invalid retained sample count")
				return
			}

			table.Co64.ChunkOffset[0] = uint64(offset)
			for i := range table.Stsz.SampleSize {
				table.Stsz.SampleSize[i] = 1
			}

			table.Stsz.SampleSize[0] = bounded - uint32(remaining)
		}

		var encoded bytes.Buffer
		if err := movie.Encode(&encoded); err != nil {
			t.Fatal(err)
		}

		// The retained format has a 28-byte ftyp and 16-byte mdat header.
		if encoded.Len() != len(old.Header)-28-16 {
			t.Fatal("fixed-width table mutation changed header length")
		}

		copy(old.Header[28:len(old.Header)-16], encoded.Bytes())

		before, err := json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}

		next, repackErr := Repack(t.Context(), old)
		if repackErr == nil && (next == nil || next.Revision != PackagingVersion || next.validateLayout() != nil) {
			t.Fatal("repacking accepted tables but produced an invalid layout")
		}

		if repackErr == nil {
			assertRepackedRoundTrip(t, next)
		}

		after, err := json.Marshal(old)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("repacking mutated its retained input", err)
		}
	})
}

func assertRepackedRoundTrip(t testing.TB, next *VirtualMP4) {
	t.Helper()

	data, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := decodeLayout(bytes.NewReader(fuzzLayoutEnvelope(data)))
	if err != nil || loaded.ID() != next.ID() || loaded.Size() != next.Size() || !bytes.Equal(loaded.Header, next.Header) {
		t.Fatal("successful repack cannot round-trip its saved representation", err)
	}
}

func TestRepackRejectsOverlappingTrackSources(t *testing.T) {
	old, movie := legacyFuzzMovie(t)
	table := movie.Traks[0].Mdia.Minf.Stbl

	audioSpan := old.Spans[1]
	if audioSpan.Output < 0 {
		t.Fatal("invalid retained audio offset")
		return
	}

	table.Co64.ChunkOffset[0] = uint64(audioSpan.Output)
	for i := range table.Stsz.SampleSize {
		table.Stsz.SampleSize[i] = 1
	}

	firstSize := audioSpan.Length - int64(len(table.Stsz.SampleSize)-1)
	if firstSize < 0 || firstSize > math.MaxUint32 {
		t.Fatal("invalid retained sample size")
		return
	}

	table.Stsz.SampleSize[0] = uint32(firstSize)

	var encoded bytes.Buffer
	if err := movie.Encode(&encoded); err != nil {
		t.Fatal(err)
	}

	if encoded.Len() != len(old.Header)-44 {
		t.Fatal("table mutation changed header length")
	}

	copy(old.Header[28:len(old.Header)-16], encoded.Bytes())
	old.setIdentity()

	input := filepath.Join(t.TempDir(), "legacy.json")
	if err := old.SaveIndex(input); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadLayout(input)
	if err != nil {
		t.Fatal("legacy map should pass its saved-format checks", err)
	}

	before, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}

	if next, repackErr := Repack(t.Context(), loaded); repackErr == nil || next != nil {
		t.Fatal("repack accepted two tracks mapped to one source", repackErr)
	}

	after, err := json.Marshal(loaded)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed repack changed the saved input", err)
	}
}

func TestRepackSavedLayoutRoundTrip(t *testing.T) {
	old, _ := legacyFuzzMovie(t)

	next, err := Repack(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}

	assertRepackedRoundTrip(t, next)

	path := filepath.Join(t.TempDir(), "current.json")
	if err = next.SaveIndex(path); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadLayout(path)
	if err != nil || loaded.ID() != next.ID() || loaded.Size() != next.Size() || !bytes.Equal(loaded.Header, next.Header) {
		t.Fatal("saved repack did not retain its byte contract", err)
	}
}
