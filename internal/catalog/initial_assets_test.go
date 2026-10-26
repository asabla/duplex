package catalog

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"duplex/internal/source"
)

func TestInitialAssetOwnershipAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalogue.sqlite")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if s != nil {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}
	})

	if err = s.EnsureChannel(t.Context(), "owner", "Owner"); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"initial", "interrupted"} {
		if err = s.SaveMetadata(t.Context(), source.Metadata{ID: id, ChannelID: "owner"}); err != nil {
			t.Fatal(err)
		}
	}

	owned, err := s.BeginInitialAsset(t.Context(), "initial")
	if err != nil || !owned {
		t.Fatal(owned, err)
	}

	if owned, err = s.BeginInitialAsset(t.Context(), "initial"); err != nil || owned {
		t.Fatal("initial job was acquired twice", owned, err)
	}

	if err = s.QueueAsset(t.Context(), "initial"); err != nil {
		t.Fatal(err)
	}

	if _, err = s.NextAsset(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("background worker stole initial work", err)
	}

	if err = s.RetryInitialAsset(t.Context(), "initial"); err != nil {
		t.Fatal(err)
	}

	if id, err := s.NextAsset(t.Context()); err != nil || id != "initial" {
		t.Fatal(id, err)
	}

	if owned, err = s.BeginInitialAsset(t.Context(), "initial"); err != nil || owned {
		t.Fatal("initial work stole existing refresh", owned, err)
	}

	if err = s.FinishAsset(t.Context(), "initial", nil); err != nil {
		t.Fatal(err)
	}

	if err = s.RetryInitialAsset(t.Context(), "initial"); err != nil {
		t.Fatal(err)
	}

	if _, err = s.NextAsset(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("retry replaced completed work", err)
	}

	if owned, err = s.BeginInitialAsset(t.Context(), "interrupted"); err != nil || !owned {
		t.Fatal(owned, err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if id, err := s.NextAsset(t.Context()); err != nil || id != "interrupted" {
		t.Fatal("restart lost interrupted initial work", id, err)
	}
}
