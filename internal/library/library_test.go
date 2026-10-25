package library

import (
	"context"
	"encoding/xml"
	"io"
	"reflect"
	"testing"
)

func TestTitlesDoNotChangeIdentity(t *testing.T) {
	e := Episode{ChannelID: "UC_source", ChannelTitle: "A & B", VideoID: "video_source", Title: "<old>", Season: 2026, Number: 7, Media: blob{id: "media", data: []byte("test")}}
	a, err := Entries([]Episode{e})
	if err != nil {
		t.Fatal(err)
	}
	e.Title = "new"
	e.ChannelTitle = "renamed"
	b, err := Entries([]Episode{e})
	if err != nil {
		t.Fatal(err)
	}
	for n := range a {
		if a[n].Path != b[n].Path || Inode(a[n].Path) != Inode(b[n].Path) {
			t.Fatal("display metadata changed identity")
		}
	}
	for _, entry := range a {
		if entry.Path[len(entry.Path)-4:] == ".nfo" {
			data, err := io.ReadAll(&representationReader{entry: entry})
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Title string `xml:"title"`
			}
			if err = xml.Unmarshal(data, &doc); err != nil {
				t.Fatal("invalid NFO XML", err)
			}
			if doc.Title != "A & B" && doc.Title != "<old>" {
				t.Fatal("metadata not preserved")
			}
		}
	}
	if _, err = Entries([]Episode{e, e}); err == nil {
		t.Fatal("duplicate episode accepted")
	}
	e.VideoID = "../escape"
	if _, err = Entries([]Episode{e}); err == nil {
		t.Fatal("path traversal accepted")
	}
	if reflect.DeepEqual(a, b) {
		t.Fatal("display metadata did not update")
	}
}

type representationReader struct {
	entry  Entry
	offset int64
}

func (r *representationReader) Read(p []byte) (int, error) {
	n, err := r.entry.Media.ReadAt(context.Background(), p, r.offset)
	r.offset += int64(n)
	return n, err
}
