package catalog

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RecordShowMetadata gives content changes their own durable modification time
// and queues a targeted Plex refresh. Unchanged bytes retain their timestamp.
func (s *Store) RecordShowMetadata(ctx context.Context, channel, digest string, modified int64) (int64, error) {
	if digest == "" {
		return 0, errors.New("show metadata identity is empty")
	}

	var result int64

	err := s.DB.QueryRowContext(ctx, `INSERT INTO show_metadata(channel_id,digest,modified) VALUES(?,?,?) ON CONFLICT(channel_id) DO UPDATE SET digest=excluded.digest,modified=MAX(show_metadata.modified+1,excluded.modified),pending=1,attempted=0,error='' WHERE digest<>excluded.digest RETURNING modified`, channel, digest, max(modified, time.Now().Unix())).Scan(&result)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.DB.QueryRowContext(ctx, `SELECT modified FROM show_metadata WHERE channel_id=?`, channel).Scan(&result)
	}

	return result, err
}

// ShowRefresh identifies one immutable metadata version awaiting Plex refresh.
type ShowRefresh struct {
	Channel string
	Digest  string
}

// NextShowRefresh reserves a single pending refresh. Failed attempts are paced
// for fifteen minutes; a new metadata version can run immediately.
func (s *Store) NextShowRefresh(ctx context.Context) (ShowRefresh, error) {
	var result ShowRefresh

	err := s.DB.QueryRowContext(ctx, `UPDATE show_metadata SET attempted=? WHERE channel_id=(SELECT channel_id FROM show_metadata WHERE pending=1 AND attempted<=? ORDER BY attempted,channel_id LIMIT 1) RETURNING channel_id,digest`, time.Now().Unix(), time.Now().Add(-15*time.Minute).Unix()).Scan(&result.Channel, &result.Digest)

	return result, err
}

// FinishShowRefresh cannot acknowledge a newer version published during work.
func (s *Store) FinishShowRefresh(ctx context.Context, job ShowRefresh, failure error) error {
	message := ""
	if failure != nil {
		message = "Plex metadata refresh failed; retry is delayed"
	}

	_, err := s.DB.ExecContext(ctx, `UPDATE show_metadata SET pending=?,error=? WHERE channel_id=? AND digest=?`, failure != nil, message, job.Channel, job.Digest)

	return err
}

// ShowRefreshStatus returns queue size and a redacted outstanding failure.
func (s *Store) ShowRefreshStatus(ctx context.Context) (pending int, failure string, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT count(*),COALESCE(MAX(error),'') FROM show_metadata WHERE pending=1`).Scan(&pending, &failure)
	return pending, failure, err
}
