package catalog

import (
	"database/sql"
	"errors"
	"testing"
)

func TestShowMetadataChangesAreDurableAndAcknowledgedByVersion(t *testing.T) {
	s := collectionStore(t)

	stamp, err := s.RecordShowMetadata(t.Context(), "owner", "first-digest", 1900000000)
	if err != nil {
		t.Fatal(err)
	}

	unchanged, err := s.RecordShowMetadata(t.Context(), "owner", "first-digest", stamp+1000)
	if err != nil || unchanged != stamp {
		t.Fatal("unrelated episode change advanced show metadata", stamp, unchanged, err)
	}

	job, err := s.NextShowRefresh(t.Context())
	if err != nil || job.Channel != "owner" || job.Digest != "first-digest" {
		t.Fatal(job, err)
	}

	if _, err = s.NextShowRefresh(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("in-flight refresh was not reserved", err)
	}

	newStamp, err := s.RecordShowMetadata(t.Context(), "owner", "new-digest", stamp-1000)
	if err != nil || newStamp <= stamp {
		t.Fatal("show content change did not advance time", newStamp, err)
	}

	if err = s.FinishShowRefresh(t.Context(), job, nil); err != nil {
		t.Fatal(err)
	}

	newJob, err := s.NextShowRefresh(t.Context())
	if err != nil || newJob.Digest != "new-digest" {
		t.Fatal("old completion lost new work", newJob, err)
	}

	if err = s.FinishShowRefresh(t.Context(), newJob, errors.New("private locator and credentials")); err != nil {
		t.Fatal(err)
	}

	if pending, failure, err := s.ShowRefreshStatus(t.Context()); err != nil || pending != 1 || failure != "Plex metadata refresh failed; retry is delayed" {
		t.Fatal(pending, failure, err)
	}

	if _, err = s.NextShowRefresh(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("failed refresh was immediately retried", err)
	}

	var message string
	if err = s.DB.QueryRowContext(t.Context(), `SELECT error FROM show_metadata WHERE channel_id='owner'`).Scan(&message); err != nil || message != "Plex metadata refresh failed; retry is delayed" {
		t.Fatal(message, err)
	}

	if _, err = s.DB.ExecContext(t.Context(), `UPDATE show_metadata SET attempted=0`); err != nil {
		t.Fatal(err)
	}

	job, err = s.NextShowRefresh(t.Context())
	if err != nil || job != newJob {
		t.Fatal(job, err)
	}

	if err = s.FinishShowRefresh(t.Context(), job, nil); err != nil {
		t.Fatal(err)
	}

	if _, err = s.NextShowRefresh(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("completed refresh was queued again", err)
	}

	if _, err = s.RecordShowMetadata(t.Context(), "owner", "", 1); err == nil {
		t.Fatal("empty content identity accepted")
	}
}
