package media

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

type indexTestSource struct {
	id   string
	data []byte
}

func (s indexTestSource) ID() string  { return s.id }
func (s indexTestSource) Size() int64 { return int64(len(s.data)) }
func (s indexTestSource) ReadAt(_ context.Context, p []byte, o int64) (int, error) {
	return bytes.NewReader(s.data).ReadAt(p, o)
}

func TestDurableIndexRejectsChangedSourcesAndCorruption(t *testing.T) {
	one, two := indexTestSource{"one", []byte("abc")}, indexTestSource{"two", []byte("def")}
	v := &VirtualMP4{Revision: PackagingVersion, Header: make([]byte, 16), Sources: []string{"one", "two"}, Length: 22, Spans: []Span{{Output: 16, SourceID: "one", Offset: 0, Length: 3, source: one}, {Output: 19, SourceID: "two", Offset: 0, Length: 3, source: two}}}
	v.setIdentity()

	file := filepath.Join(t.TempDir(), "index.json")
	if e := v.SaveIndex(file); e != nil {
		t.Fatal(e)
	}

	loaded, e := LoadIndex(file, map[string]Representation{"one": one, "two": two})
	if e != nil || loaded.ID() != v.ID() {
		t.Fatal(loaded, e)
	}

	b := make([]byte, 6)
	if _, e = loaded.ReadAt(context.Background(), b, 16); e != nil || string(b) != "abcdef" {
		t.Fatal(string(b), e)
	}

	if _, e = LoadIndex(file, map[string]Representation{"changed": one, "two": two}); e == nil {
		t.Fatal("changed source accepted")
	}

	data, err := os.ReadFile(file) //nolint:gosec // G304: This reads the generated test index in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	data = bytes.Replace(data, []byte(`"offset":0`), []byte(`"offset":1`), 1)
	if e = os.WriteFile(file, data, 0o600); e != nil { //nolint:gosec // G703: This corrupts a generated packaging index in t.TempDir to test rejection.
		t.Fatal(e)
	}

	if _, e = LoadIndex(file, map[string]Representation{"one": one, "two": two}); e == nil {
		t.Fatal("corrupted mapping accepted")
	}
}
