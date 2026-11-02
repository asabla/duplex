package media

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestLayoutHeadersAreLocalAndPayloadMustBeBound(t *testing.T) {
	one, two := indexTestSource{"one", []byte("abc")}, indexTestSource{"two", []byte("def")}
	v := &VirtualMP4{Revision: PackagingVersion, Header: []byte("original-header!"), Sources: []string{"one", "two"}, Length: 22, Spans: []Span{{Output: 16, SourceID: "one", Length: 3, source: one}, {Output: 19, SourceID: "two", Length: 3, source: two}}}
	v.setIdentity()

	file := filepath.Join(t.TempDir(), "layout.json")
	if err := v.SaveIndex(file); err != nil {
		t.Fatal(err)
	}

	local, err := LoadLayout(file)
	if err != nil || local.ID() != v.ID() || local.SourceLengths["one"] != 3 {
		t.Fatal(local, err)
	}

	header := make([]byte, 16)
	if _, err = local.ReadAt(t.Context(), header, 0); err != nil || !bytes.Equal(header, v.Header) {
		t.Fatal("local header differs", err)
	}

	if _, err = local.ReadAt(t.Context(), make([]byte, 6), 16); err == nil {
		t.Fatal("unbound payload was fabricated")
	}

	local.BindLayout(func(id string, _ int64) Representation {
		if id == "one" {
			return one
		}

		return two
	})

	payload := make([]byte, 6)
	if _, err = local.ReadAt(t.Context(), payload, 16); err != nil || string(payload) != "abcdef" {
		t.Fatal(string(payload), err)
	}

	if _, err = local.ReadSource(t.Context(), "unknown", payload, 0); err == nil {
		t.Fatal("unknown track was accepted")
	}

	for _, mutate := range []func(*VirtualMP4){
		func(v *VirtualMP4) { v.Sources[1] = "one" },
		func(v *VirtualMP4) { v.Spans[0].Output++ },
		func(v *VirtualMP4) { v.Spans[0].Offset = -1 },
		func(v *VirtualMP4) { v.Spans[0].Length = 0 },
		func(v *VirtualMP4) { v.Spans[0].SourceID = "unknown" },
		func(v *VirtualMP4) { v.Spans[0].Offset = 2 },
	} {
		bad := *v
		bad.Sources = append([]string(nil), v.Sources...)
		bad.Spans = append([]Span(nil), v.Spans...)
		mutate(&bad)

		if err = bad.SaveIndex(file); err != nil {
			t.Fatal(err)
		}

		if _, err = LoadLayout(file); err == nil {
			t.Fatal("invalid layout accepted")
		}
	}
}
