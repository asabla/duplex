package catalog

import (
	"fmt"
	"testing"

	"duplex/internal/config"
)

func TestSelectionReviewPagesShowChangedTitlesAndRetainIdentity(t *testing.T) {
	s := collectionStore(t)

	for n := range 60 {
		id := fmt.Sprintf("video%06d", n)
		preparedVideo(t, s, id, fmt.Sprintf("Controlled title %02d", n))

		if n < 30 {
			if err := s.MarkUploadedSelection(t.Context(), id); err != nil {
				t.Fatal(err)
			}
		}
	}

	before, err := s.Video(t.Context(), "video000000")
	if err != nil {
		t.Fatal(err)
	}

	ch := config.Channel{ID: "uploader", Indexing: &config.Selection{Mode: "all"}}
	for _, view := range []string{"selected", "added"} {
		p, err := s.PreviewUploadsPage(t.Context(), ch, view, 99)
		if err != nil || p.Selected != 60 || p.Added != 30 || p.Removed != 0 || p.Page != p.Pages || len(p.Items) != map[string]int{"selected": 10, "added": 5}[view] {
			t.Fatal(view, p, err)
		}

		if view == "added" && p.Items[0].ID != "video000055" {
			t.Fatal("additions included an unchanged recording", p.Items)
		}
	}

	ch.Content = &config.ContentPolicy{Uploads: config.Selection{Mode: "skip"}, Shorts: config.Selection{Mode: "skip"}, Replays: config.Selection{Mode: "skip"}}
	ch.VideoOverrides = map[string]string{"video000001": "exclude"}

	p, err := s.PreviewUploadsPage(t.Context(), ch, "removed", 2)
	if err != nil || p.Removed != 30 || p.Page != 2 || len(p.Items) != 5 || p.Items[0].Title != "Controlled title 25" {
		t.Fatal(p, err)
	}

	p, err = s.PreviewUploadsPage(t.Context(), ch, "removed", 0)
	if err != nil || p.Items[1].Override != "exclude" {
		t.Fatal(p, err)
	}

	if _, err = s.PreviewUploadsPage(t.Context(), ch, "invalid", 1); err == nil {
		t.Fatal("invalid review accepted")
	}

	after, err := s.Video(t.Context(), "video000000")
	if err != nil || before != after {
		t.Fatal("preview changed recording state", before, after, err)
	}
}

func TestRecordingSearchIsBoundedOwnedAndUsesDraftRules(t *testing.T) {
	s := collectionStore(t)

	for n := range 23 {
		id := fmt.Sprintf("video%06d", n)
		preparedVideo(t, s, id, "Controlled title "+id)
	}

	ch := config.Channel{ID: "uploader", VideoOverrides: map[string]string{"video000020": "keep"}}

	page, err := s.SearchRecordings(t.Context(), ch, "CONTROLLED", 99)
	if err != nil || page.Total != 23 || page.Page != 3 || len(page.Items) != 3 || page.Items[0].Override != "keep" {
		t.Fatal(page, err)
	}

	page, err = s.SearchRecordings(t.Context(), config.Channel{ID: "owner"}, "", 1)
	if err != nil || page.Total != 0 {
		t.Fatal("search included a playlist's foreign uploader", page, err)
	}

	page, err = s.SearchRecordings(t.Context(), ch, "video000020", -1)
	if err != nil || page.Total != 1 || page.Items[0].Override != "keep" {
		t.Fatal(page, err)
	}

	ids := []string{"video000020", "missing"}

	kinds, err := s.RecordingKinds(t.Context(), ids)
	if err != nil || len(kinds) != 1 || kinds["video000020"] != "" {
		t.Fatal("unknown type was guessed", kinds, err)
	}

	ids = make([]string, 101)
	ids[100] = "video000020"

	kinds, err = s.RecordingKinds(t.Context(), ids)
	if err != nil || len(kinds) != 0 {
		t.Fatal("kind lookup exceeded its page bound", kinds, err)
	}
}
