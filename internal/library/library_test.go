package library

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
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

func TestRangeCopiesValidateBoundsAndPropagateReadErrors(t *testing.T) {
	source := blob{id: "fixture", data: []byte("abcdefgh")}

	var output bytes.Buffer
	if err := CopyRange(t.Context(), &output, source, 3, 4); err != nil || output.String() != "defg" {
		t.Fatal(output.String(), err)
	}

	for _, value := range [][2]int64{{-1, 1}, {0, -1}, {7, 2}, {9, 0}} {
		output.Reset()

		if err := CopyRange(t.Context(), &output, source, value[0], value[1]); err == nil || output.Len() != 0 {
			t.Fatal(value, output.String(), err)
		}
	}

	if _, err := ShowMetadata("../unsafe", "Title", nil, 0); err == nil {
		t.Fatal("unsafe channel path accepted")
	}

	entry, err := ShowMetadata("channel", "invalid\x01", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	output.Reset()

	if err = CopyRange(t.Context(), &output, entry.Media, 0, entry.Media.Size()); err != nil {
		t.Fatal(err)
	}

	var metadata struct {
		Title string `xml:"title"`
	}
	if err = xml.Unmarshal(output.Bytes(), &metadata); err != nil || bytes.Contains(output.Bytes(), []byte{1}) {
		t.Fatal("unsafe XML emitted", output.String(), err)
	}

	if _, err := EpisodeMetadata(Episode{ChannelID: "channel", VideoID: "video", Season: 0, Number: 1}); err == nil {
		t.Fatal("invalid episode slot accepted")
	}
}

type failedRange struct{ err error }

func (r failedRange) ID() string                                         { return "failed" }
func (r failedRange) Size() int64                                        { return 8 }
func (r failedRange) ReadAt(context.Context, []byte, int64) (int, error) { return 0, r.err }
func TestCopyRangePreservesReadFailures(t *testing.T) {
	sentinel := errors.New("fixture source failed")

	var output bytes.Buffer
	if err := CopyRange(t.Context(), &output, failedRange{sentinel}, 0, 1); !errors.Is(err, sentinel) || output.Len() != 0 {
		t.Fatal(output.String(), err)
	}
}
