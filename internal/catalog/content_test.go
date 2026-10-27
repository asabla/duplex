package catalog

import (
	"fmt"
	"path/filepath"
	"testing"

	"duplex/internal/config"
	"duplex/internal/source"
)

func TestContentClassificationUsesSourceEvidenceAndUploaderOwnership(t *testing.T) {
	s := collectionStore(t)

	prepared := preparedVideo(t, s, "video000001", "Controlled recording")
	for _, kind := range []string{"upload", "short", "upload"} {
		if err := s.Discover(t.Context(), "uploader", []source.Entry{{ID: prepared.Metadata.ID, Kind: kind}}); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.Discover(t.Context(), "owner", []source.Entry{{ID: prepared.Metadata.ID, Kind: "replay"}}); err != nil {
		t.Fatal(err)
	}

	if kind, err := s.RecordingKind(t.Context(), prepared.Metadata.ID); err != nil || kind != "short" {
		t.Fatal("generic or foreign tab evidence replaced a Short", kind, err)
	}

	prepared.Metadata.LiveStatus = "was_live"
	if err := s.SaveMetadata(t.Context(), prepared.Metadata); err != nil {
		t.Fatal(err)
	}

	if kind, err := s.RecordingKind(t.Context(), prepared.Metadata.ID); err != nil || kind != "replay" {
		t.Fatal("completed broadcast metadata did not identify a replay", kind, err)
	}
}

func TestSelectionPreviewIsBoundedAndUnknownTypesAreExplicit(t *testing.T) {
	s := collectionStore(t)

	for n := range 30 {
		id := fmt.Sprintf("video%06d", n)
		preparedVideo(t, s, id, id)
	}

	ch := config.Channel{ID: "uploader", Indexing: &config.Selection{Mode: "all"}}

	p, err := s.PreviewUploads(t.Context(), ch)
	if err != nil || p.Selected != 30 || p.UnknownKind != 30 || len(p.Items) != 25 {
		t.Fatal(p, err)
	}

	ch.Content = &config.ContentPolicy{Uploads: config.Selection{Mode: "all"}, Shorts: config.Selection{Mode: "all"}, Replays: config.Selection{Mode: "all"}}
	ch.VideoOverrides = map[string]string{"video000000": "keep"}

	p, err = s.PreviewUploads(t.Context(), ch)
	if err != nil || p.Selected != 1 || p.UnknownKind != 30 || p.Items[0].Override != "keep" {
		t.Fatal("unknown content was guessed or Keep did not apply", p, err)
	}

	ch.VideoOverrides["video000001"] = "invalid"
	if _, err = s.PreviewUploads(t.Context(), ch); err == nil {
		t.Fatal("invalid rules were previewed")
	}
}

func TestSchemaEightUpgradeRetainsSlotsAndLeavesUnknownTypesUnknown(t *testing.T) {
	file := filepath.Join(t.TempDir(), "catalogue.sqlite")

	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"video000000", "video000001"} {
		p := preparedVideo(t, s, id, id)
		if id == "video000001" {
			p.Metadata.LiveStatus = "was_live"
			if err = s.SaveMetadata(t.Context(), p.Metadata); err != nil {
				t.Fatal(err)
			}
		}
	}

	before, err := s.Video(t.Context(), "video000001")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.DB.ExecContext(t.Context(), `DROP TABLE upload_rule_state; DROP TABLE video_content; PRAGMA user_version=8`); err != nil {
		t.Fatal(err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	after, err := s.Video(t.Context(), "video000001")
	if err != nil || after.Number != before.Number || after.Revision != before.Revision {
		t.Fatal("schema upgrade changed media identity", before, after, err)
	}

	if kind, err := s.RecordingKind(t.Context(), "video000001"); err != nil || kind != "replay" {
		t.Fatal(kind, err)
	}

	if kind, err := s.RecordingKind(t.Context(), "video000000"); err != nil || kind != "" {
		t.Fatal("upgrade guessed a content kind", kind, err)
	}
}
