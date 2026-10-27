package catalog

import "testing"

func TestUploadDirectoryChangesOnRemovalAndReturnOnly(t *testing.T) {
	s := collectionStore(t)
	for _, id := range []string{"a", "b"} {
		preparedVideo(t, s, id, id)

		if err := s.ExposePrepared(t.Context(), id); err != nil {
			t.Fatal(err)
		}

		if err := s.MarkUploadedSelection(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}

	for _, query := range []string{`UPDATE videos SET updated=100 WHERE id='a'`, `UPDATE videos SET updated=200 WHERE id='b'`, `UPDATE channels SET updated=0`} {
		if _, err := s.DB.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}

	visible := Visibility{Channels: []string{"uploader"}}

	before, err := s.ProjectionChanged(t.Context(), visible, "uploader")
	if err != nil {
		t.Fatal(err)
	}

	video, err := s.Video(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}

	if err = s.StageDiscovery(t.Context(), 1, "uploads", "b", 1); err != nil {
		t.Fatal(err)
	}

	if err = s.ReconcileDiscovery(t.Context(), 1, "uploads", "uploader", 0); err != nil {
		t.Fatal(err)
	}

	removed, err := s.ProjectionChanged(t.Context(), visible, "uploader")
	if err != nil || removed <= before {
		t.Error("removing an older upload did not advance its directory", before, removed, err)
	}

	if err = s.MarkUploadedSelection(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}

	returned, err := s.ProjectionChanged(t.Context(), visible, "uploader")
	if err != nil || returned <= removed {
		t.Error("returning an older upload did not advance its directory", removed, returned, err)
	}

	for i, id := range []string{"a", "b"} {
		if err = s.StageDiscovery(t.Context(), 2, "uploads", id, i+1); err != nil {
			t.Fatal(err)
		}
	}

	if err = s.ReconcileDiscovery(t.Context(), 2, "uploads", "uploader", 0); err != nil {
		t.Fatal(err)
	}

	unchanged, err := s.ProjectionChanged(t.Context(), visible, "uploader")
	if err != nil || unchanged != returned {
		t.Fatal("unchanged selection advanced directory time", returned, unchanged, err)
	}

	after, err := s.Video(t.Context(), "a")
	if err != nil || after.Revision != video.Revision || after.Number != video.Number || after.Updated != video.Updated {
		t.Fatal("selection changed recording identity or file time", video, after, err)
	}
}
