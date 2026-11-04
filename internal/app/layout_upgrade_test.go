package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/media"
	"duplex/internal/source"
)

func TestOfflineLayoutUpgradePreviewLockIdentityAndRestart(t *testing.T) {
	a, adapter, _ := mediaPipeline(t)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	if err := a.prepareRecording(t.Context(), "video000000", false); err != nil {
		t.Fatal(err)
	}

	v, err := a.Catalog.Video(t.Context(), "video000000")
	if err != nil {
		t.Fatal(err)
	}

	current, ok := a.reps[v.Revision].(*media.VirtualMP4)
	if !ok {
		t.Fatal("fixture did not prepare a virtual MP4")
	}

	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}

	var legacy media.VirtualMP4
	if err = json.Unmarshal(encoded, &legacy); err != nil {
		t.Fatal(err)
	}

	legacy.Revision = "progressive-mp4-v1"
	hash := sha256.New()
	hash.Write([]byte(legacy.Revision))

	for _, id := range legacy.Sources {
		hash.Write([]byte(id))
	}

	hash.Write(legacy.Header)
	legacyID := hex.EncodeToString(hash.Sum(nil))
	// Retain exact sizes from the prepared disk index, including non-payload boxes.
	saved, err := media.LoadLayout(filepath.Join(a.original.IndexDir, v.Revision+".mp4-index.json"))
	if err != nil {
		t.Fatal(err)
	}

	legacy.SourceLengths = saved.SourceLengths

	oldPath := filepath.Join(a.original.IndexDir, legacyID+".mp4-index.json")
	if err = legacy.SaveIndex(oldPath); err != nil {
		t.Fatal(err)
	}

	old, err := media.LoadLayout(oldPath)
	if err != nil || old.ID() != legacyID {
		t.Fatal(old, err)
	}

	metadata := adapter.metadata[v.ID]

	candidate := source.Candidates(metadata, 2160)[0]
	if err = a.persistRepresentation(metadata, candidate, old, filepath.Join(a.original.IndexDir, "temporary-alias.json")); err != nil {
		t.Fatal(err)
	}

	key := sha256.Sum256([]byte(metadata.ID + ":" + candidate.Video.ID + ":" + candidate.Audio.ID))

	aliasPath := filepath.Join(a.original.IndexDir, hex.EncodeToString(key[:])+".json")
	if err = old.SaveIndex(aliasPath); err != nil {
		t.Fatal(err)
	}

	if err = a.Catalog.SavePrepared(t.Context(), catalog.Prepared{Metadata: metadata, Revision: legacyID, Size: old.Size(), Height: v.Height, RequestedHeight: 2160}); err != nil {
		t.Fatal(err)
	}

	if err = a.Catalog.BindOccurrence(t.Context(), "youtube-video:"+v.ID, "controlled-server", "controlled-key", "controlled-guid"); err != nil {
		t.Fatal(err)
	}

	oldVideo, err := a.Catalog.Video(t.Context(), v.ID)
	if err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(oldPath) //nolint:gosec // G304: This file belongs to the controlled fixture beneath t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	c := a.Config.Snapshot().Config
	if err = os.MkdirAll(c.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err = a.Catalog.Backup(t.Context(), filepath.Join(c.DataDir, "catalogue.sqlite")); err != nil {
		t.Fatal(err)
	}

	maintenanceStore, err := catalog.Open(filepath.Join(c.DataDir, "catalogue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := maintenanceStore.Close(); err != nil {
			t.Error(err)
		}
	})

	a.Catalog = maintenanceStore

	if _, err = UpgradeLayouts(t.Context(), c, "", true); err == nil {
		t.Fatal("maintenance ignored running service ownership")
	}

	if err = a.Cache.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := UpgradeLayouts(t.Context(), c, "", false)
	if err != nil || report.Eligible != 1 || report.Upgraded != 0 {
		t.Fatal(report, err)
	}

	afterPreview, err := a.Catalog.Video(t.Context(), v.ID)
	if err != nil || afterPreview != oldVideo {
		t.Fatal("preview changed catalogue", afterPreview, err)
	}

	report, err = UpgradeLayouts(t.Context(), c, "", true)
	if err != nil || report.Upgraded != 1 {
		t.Fatal(report, err)
	}

	after, err := a.Catalog.Video(t.Context(), v.ID)
	if err != nil || after.Revision != v.Revision || after.Size != v.Size || after.Year != oldVideo.Year || after.Number != oldVideo.Number || after.Title != oldVideo.Title {
		t.Fatal("upgrade changed occurrence identity", after, err)
	}

	var plexKey, guid string
	if err = a.Catalog.DB.QueryRowContext(t.Context(), `SELECT rating_key,guid FROM episode_bindings WHERE occurrence_id=?`, "youtube-video:"+v.ID).Scan(&plexKey, &guid); err != nil || plexKey != "controlled-key" || guid != "controlled-guid" {
		t.Fatal("Plex binding changed", plexKey, guid, err)
	}

	afterBytes, err := os.ReadFile(oldPath) //nolint:gosec // G304: This file belongs to the controlled fixture beneath t.TempDir.
	if err != nil || !bytes.Equal(before, afterBytes) {
		t.Fatal("old byte map was overwritten", err)
	}

	alias, err := media.LoadLayout(aliasPath)
	if err != nil || alias.ID() != v.Revision {
		t.Fatal("reusable alias would downgrade upgraded media", alias, err)
	}

	report, err = UpgradeLayouts(t.Context(), c, v.ID, true)
	if err != nil || report.Upgraded != 0 || report.Skipped != 1 {
		t.Fatal("repeat was not idempotent", report, err)
	}

	// Simulate interruption after immutable files and alias, before catalogue commit.
	if _, err = a.Catalog.DB.ExecContext(t.Context(), `UPDATE videos SET revision=?,size=? WHERE id=?`, legacyID, old.Size(), v.ID); err != nil {
		t.Fatal(err)
	}

	report, err = UpgradeLayouts(t.Context(), c, v.ID, true)
	if err != nil || report.Upgraded != 1 {
		t.Fatal("interrupted upgrade did not resume", report, err)
	}
}
