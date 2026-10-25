package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/source"
)

type failingSource struct{ metadata bool }

func (f failingSource) ResolveChannel(context.Context, string) (source.Channel, error) {
	panic("unused")
}
func (f failingSource) Resolve(context.Context, string) (source.Metadata, error) { panic("unused") }
func (f failingSource) Metadata(context.Context, string) (source.Metadata, error) {
	return source.Metadata{}, errors.New("source failed")
}

func (f failingSource) Enumerate(context.Context, string) (source.Channel, error) {
	c := source.Channel{ID: "UCSMOQeBJ2RAnuFungnQOxLg", Title: "Sample", Entries: []source.Entry{{ID: "aqz-KE-bpKQ"}}}
	if f.metadata {
		return c, nil
	}
	return c, errors.New("partial enumeration failed")
}

func TestFailedRefreshRetainsCompleteCatalogue(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if e := config.Create(file, filepath.Join(dir, "state")); e != nil {
		t.Fatal(e)
	}
	manager, _ := config.Open(file)
	snap := manager.Snapshot()
	id := "UCSMOQeBJ2RAnuFungnQOxLg"
	snap.Config.Channels = []config.Channel{{ID: id, URL: "https://www.youtube.com/channel/" + id, Limit: 50, Enabled: true}}
	if e := manager.Save(snap.Config, snap.Revision); e != nil {
		t.Fatal(e)
	}
	db, e := catalog.Open(filepath.Join(dir, "catalogue.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer closeTest(t, db)
	ch := source.Channel{ID: id, Title: "Original", Entries: []source.Entry{{ID: "aqz-KE-bpKQ"}, {ID: "k0ILMhbWHNQ"}}}
	if e = db.EnsureChannel(ctx, ch.ID, ch.Title); e != nil {
		t.Fatal(e)
	}
	if e = db.Discover(ctx, ch.ID, ch.Entries); e != nil {
		t.Fatal(e)
	}
	before, _ := db.Videos(ctx)
	for _, metadataFailure := range []bool{false, true} {
		a := &App{Config: manager, Catalog: db, Source: failingSource{metadata: metadataFailure}}
		if a.Sync(ctx, id) == nil {
			t.Fatal("partial refresh succeeded")
		}
		after, _ := db.Videos(ctx)
		if len(after) != len(before) {
			t.Fatal("partial refresh removed entries")
		}
		channels, _ := db.Channels(ctx)
		if channels[0].Title != "Original" {
			t.Fatal("partial refresh updated staged channel")
		}
	}
}

func TestOriginalCaptionConversion(t *testing.T) {
	input := []byte("WEBVTT\n\n00:01.000 --> 00:03.500 align:start\n<c>Original</c><00:01.500> &amp; audio\n\n")
	want := "1\n00:00:01,000 --> 00:00:03,500\nOriginal & audio\n\n"
	if got := string(VTTtoSRT(input)); got != want {
		t.Fatalf("caption conversion: %q", got)
	}
}
