package media

import (
	"bytes"
	"encoding/json"
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

	f.Fuzz(func(t *testing.T, field uint8, value uint64) {
		old, movie := legacyFuzzMovie(t)
		track := movie.Traks[0]
		table := track.Mdia.Minf.Stbl
		bounded := uint32(value & 0xffffffff)

		switch field % 5 {
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

		after, err := json.Marshal(old)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("repacking mutated its retained input", err)
		}
	})
}
