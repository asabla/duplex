package catalog

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"duplex/internal/source"
)

func TestCatalogueKeepsPublicArtworkAndPacesRechecks(t *testing.T) {
	s := collectionStore(t)

	m := source.Metadata{ID: "fixture", ChannelID: "owner", Duration: 60, Thumbnail: "https://i.ytimg.com/poster?signature=private", Formats: []source.Format{{URL: "https://r1.googlevideo.com/videoplayback?secret=private"}}}
	if err := s.SaveMetadata(t.Context(), m); err != nil {
		t.Fatal(err)
	}

	stored, err := s.CachedMetadata(t.Context(), m.ID)
	if err != nil || stored.Thumbnail != "https://i.ytimg.com/poster" || len(stored.Formats) != 0 || strings.Contains(stored.Thumbnail, "private") {
		t.Fatal(stored, err)
	}

	if err = s.QueueAsset(t.Context(), m.ID); err != nil {
		t.Fatal(err)
	}

	if err = s.FinishAsset(t.Context(), m.ID, nil); err != nil {
		t.Fatal(err)
	}

	if _, err = s.NextAsset(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("fresh assets immediately rechecked", err)
	}

	if _, err = s.DB.ExecContext(t.Context(), `UPDATE asset_jobs SET updated=? WHERE video_id=?`, time.Now().Add(-7*time.Hour).Unix(), m.ID); err != nil {
		t.Fatal(err)
	}

	if id, err := s.NextAsset(t.Context()); err != nil || id != m.ID {
		t.Fatal("genuine image changes could never be checked", id, err)
	}
}
