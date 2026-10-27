package catalog

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *Store) migrateEpisodeMetadata(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS episode_metadata(video_id TEXT PRIMARY KEY REFERENCES videos(id),digest TEXT NOT NULL,pending INTEGER NOT NULL DEFAULT 1,attempted INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS pending_episode_metadata ON episode_metadata(pending,attempted,video_id)`,
		`CREATE INDEX IF NOT EXISTS episode_video_bindings ON episode_bindings(video_id,server,seen_run,occurrence_id)`,
		`PRAGMA user_version=7`,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// EpisodeRefresh identifies one durable version of optional episode assets.
type EpisodeRefresh struct {
	Video  string
	Digest string
}

// RecordEpisodeAssets queues only changed content, retaining failed work on a no-op.
func (s *Store) RecordEpisodeAssets(ctx context.Context, video, digest string) (bool, error) {
	if digest == "" {
		return false, errors.New("episode asset identity is empty")
	}

	var current string

	err := s.scanProjectionRow(ctx, `SELECT digest FROM episode_metadata WHERE video_id=?`, []any{video}, &current)
	if err == nil && current == digest {
		return false, nil
	}

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	result, err := s.DB.ExecContext(ctx, `INSERT INTO episode_metadata(video_id,digest) VALUES(?,?) ON CONFLICT(video_id) DO UPDATE SET digest=excluded.digest,pending=1,attempted=0,error='' WHERE digest<>excluded.digest`, video, digest)
	if err != nil {
		return false, err
	}

	count, err := result.RowsAffected()

	return count > 0, err
}

// NextEpisodeRefresh reserves work after Plex has observed an episode. Failures
// retry after fifteen minutes; changed assets can run immediately.
func (s *Store) NextEpisodeRefresh(ctx context.Context, server string) (EpisodeRefresh, error) {
	var job EpisodeRefresh

	err := s.DB.QueryRowContext(ctx, `UPDATE episode_metadata SET attempted=? WHERE video_id=(SELECT m.video_id FROM episode_metadata m WHERE pending=1 AND attempted<=? AND EXISTS(SELECT 1 FROM episode_bindings b WHERE b.video_id=m.video_id AND b.server=? AND b.seen_run=(SELECT MAX(seen_run) FROM episode_bindings WHERE server=?)) ORDER BY attempted,video_id LIMIT 1) RETURNING video_id,digest`, time.Now().Unix(), time.Now().Add(-15*time.Minute).Unix(), server, server).Scan(&job.Video, &job.Digest)

	return job, err
}

// EpisodeRefreshPage returns bounded occurrence keys from the last observation.
func (s *Store) EpisodeRefreshPage(ctx context.Context, server, video, after string, limit int) ([]ObservedEpisode, error) {
	if limit < 1 || limit > 128 {
		return nil, errors.New("invalid episode metadata page size")
	}

	rows, err := s.DB.QueryContext(ctx, `SELECT occurrence_id,rating_key,guid,current_index FROM episode_bindings WHERE video_id=? AND server=? AND seen_run=(SELECT MAX(seen_run) FROM episode_bindings WHERE server=?) AND occurrence_id>? ORDER BY occurrence_id LIMIT ?`, video, server, server, after, limit)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	result := []ObservedEpisode{}

	for rows.Next() {
		var item ObservedEpisode
		if err = rows.Scan(&item.Occurrence, &item.Key, &item.GUID, &item.Index); err != nil {
			return nil, err
		}

		result = append(result, item)
	}

	return result, rows.Err()
}

// FinishEpisodeRefresh cannot acknowledge a newer asset version.
func (s *Store) FinishEpisodeRefresh(ctx context.Context, job EpisodeRefresh, failure error) error {
	message := ""
	if failure != nil {
		message = "Plex episode metadata refresh failed; retry is delayed"
	}

	_, err := s.DB.ExecContext(ctx, `UPDATE episode_metadata SET pending=?,error=? WHERE video_id=? AND digest=?`, failure != nil, message, job.Video, job.Digest)

	return err
}

// EpisodeRefreshStatus returns queue size and a redacted outstanding failure.
func (s *Store) EpisodeRefreshStatus(ctx context.Context) (pending int, failure string, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT count(*),COALESCE(MAX(error),'') FROM episode_metadata WHERE pending=1`).Scan(&pending, &failure)
	return pending, failure, err
}
