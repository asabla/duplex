package media

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"testing"
)

//go:embed testdata/legacy-layout.json
var legacyLayoutSeed []byte

//go:embed testdata/current-layout.json
var currentLayoutSeed []byte

func TestRetainedLayoutSeedsReachCompleteRepacking(t *testing.T) {
	old, err := decodeLayout(bytes.NewReader(fuzzLayoutEnvelope(legacyLayoutSeed)))
	if err != nil {
		t.Fatal(err)
	}

	want, err := decodeLayout(bytes.NewReader(fuzzLayoutEnvelope(currentLayoutSeed)))
	if err != nil {
		t.Fatal(err)
	}

	next, err := Repack(t.Context(), old)
	if err != nil || next.ID() != want.ID() || next.Size() != want.Size() || !bytes.Equal(next.Header, want.Header) {
		t.Fatal("retained seed does not exercise a complete legacy upgrade", err)
	}

	if len(next.Spans) <= len(old.Spans) {
		t.Fatal("retained seed no longer splits coarse source chunks")
	}
}

func FuzzLayout(f *testing.F) {
	f.Add([]byte("invalid JSON"), int64(0))
	f.Add([]byte(`{}`), int64(-1))

	for _, data := range [][]byte{legacyLayoutSeed, currentLayoutSeed} {
		layout, err := decodeLayout(bytes.NewReader(fuzzLayoutEnvelope(data)))
		if err != nil {
			f.Fatal(err)
		}

		for _, offset := range []int64{0, int64(len(layout.Header) - 1), int64(len(layout.Header)), layout.Size() - 1} {
			f.Add(data, offset)
		}
	}

	for _, revision := range []string{legacyPackagingVersion, PackagingVersion} {
		layout := &VirtualMP4{Revision: revision, Header: make([]byte, 16), Sources: []string{"one", "two"}, Length: 22, Spans: []Span{{Output: 16, SourceID: "one", Length: 3}, {Output: 19, SourceID: "two", Length: 3}}}

		data, err := json.Marshal(layout)
		if err != nil {
			f.Fatal(err)
		}

		f.Add(data, int64(0))
		f.Add(data, int64(15))
		f.Add(data, int64(19))
	}

	f.Fuzz(func(t *testing.T, data []byte, offset int64) {
		if len(data) > 1<<20 {
			return
		}

		// Recompute the checksum so mutations reach structural validation.
		layout, err := decodeLayout(bytes.NewReader(fuzzLayoutEnvelope(data)))
		if err != nil {
			return
		}

		output := make([]byte, 32)

		n, readErr := layout.ReadAt(t.Context(), output, offset)
		if n < 0 || n > len(output) || (readErr == nil && n != len(output)) {
			t.Fatal("invalid read length", n)
		}

		if n > 0 && (offset < 0 || offset > int64(len(layout.Header))-int64(n) || !bytes.Equal(output[:n], layout.Header[offset:offset+int64(n)])) {
			t.Fatal("unbound layout returned bytes outside its retained header")
		}

		if layout.Revision == legacyPackagingVersion {
			next, err := Repack(t.Context(), layout)
			if err == nil && (next == nil || next.Revision != PackagingVersion) {
				t.Fatal("repack did not produce a current layout")
			}
		}
	})
}

func fuzzLayoutEnvelope(data []byte) []byte {
	data = bytes.TrimSpace(data)
	hash := sha256.Sum256(data)
	envelope := append([]byte(`{"checksum":"`+hex.EncodeToString(hash[:])+`","data":`), data...)

	return append(envelope, '}')
}
