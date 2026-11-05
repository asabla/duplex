package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func FuzzLayout(f *testing.F) {
	f.Add([]byte("invalid JSON"), int64(0))
	f.Add([]byte(`{}`), int64(-1))

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
		hash := sha256.Sum256(data)
		envelope := append([]byte(`{"checksum":"`+hex.EncodeToString(hash[:])+`","data":`), data...)
		envelope = append(envelope, '}')

		layout, err := decodeLayout(bytes.NewReader(envelope))
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
