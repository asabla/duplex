package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"duplex/internal/source"
)

// Video returns one canonical record without loading the catalogue.
func (s *Store) Video(ctx context.Context, id string) (Video, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+videoColumns+` FROM videos WHERE id=?`, id)
	if err != nil {
		return Video{}, err
	}
	items, err := readVideos(rows)
	if err != nil {
		return Video{}, err
	}
	if len(items) == 0 {
		return Video{}, sql.ErrNoRows
	}
	return items[0], nil
}

// CachedMetadata returns durable, locator-free metadata for stable recordings.
func (s *Store) CachedMetadata(ctx context.Context, id string) (source.Metadata, error) {
	var data []byte
	err := s.DB.QueryRowContext(ctx, `SELECT metadata FROM metadata_revisions WHERE video_id=? ORDER BY revision DESC LIMIT 1`, id).Scan(&data)
	if err != nil {
		return source.Metadata{}, err
	}
	var m source.Metadata
	if err = json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	v, err := s.Video(ctx, id)
	if err != nil {
		return m, err
	}
	m.ID = v.ID
	m.ChannelID = v.ChannelID
	m.Title = v.Title
	m.Description = v.Description
	m.Timestamp = float64(v.Published)
	switch v.State {
	case "replay-ready":
		m.LiveStatus = "was_live"
	case "recorded":
		m.LiveStatus = "not_live"
	}
	return m, nil
}

// QueuePreparation deduplicates work and allows an explicit priority increase.
func (s *Store) QueuePreparation(ctx context.Context, id string, priority int) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	_, err = tx.ExecContext(ctx, `INSERT INTO preparation_jobs(video_id,state,priority,updated) VALUES(?,'queued',?,?) ON CONFLICT(video_id) DO UPDATE SET priority=MAX(priority,excluded.priority),state=CASE WHEN preparation_jobs.state IN ('failed','skipped','waiting','ready') THEN 'queued' ELSE preparation_jobs.state END,updated=excluded.updated`, id, priority, time.Now().Unix())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE videos SET preparation=(SELECT state FROM preparation_jobs WHERE video_id=?) WHERE id=?`, id, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimPreparation atomically reserves one durable queued item.
func (s *Store) ClaimPreparation(ctx context.Context) (string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer rollback(tx)
	var id string
	if err = tx.QueryRowContext(ctx, `SELECT video_id FROM preparation_jobs WHERE state='queued' ORDER BY priority DESC,updated,video_id LIMIT 1`).Scan(&id); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE preparation_jobs SET state='resolving' WHERE video_id=?`, id); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

// IsSelected applies the active YAML projection to one canonical video.
func (s *Store) IsSelected(ctx context.Context, id string, v Visibility) (bool, error) {
	cte, args := selectedCTE(v)
	args = append(args, id)
	var selected bool
	err := s.DB.QueryRowContext(ctx, cte+`SELECT EXISTS(SELECT 1 FROM selected WHERE id=?)`, args...).Scan(&selected)
	return selected, err
}

// PendingPreparations returns the queue depth without loading task bodies.
func (s *Store) PendingPreparations(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM preparation_jobs WHERE state IN ('queued','resolving','preparing')`).Scan(&n)
	return n, err
}

// ResetPreparations restores selected source leases after process restart.
func (s *Store) ResetPreparations(ctx context.Context, v Visibility) error {
	cte, args := selectedCTE(v)
	_, err := s.DB.ExecContext(ctx, cte+`INSERT INTO preparation_jobs(video_id,state,priority,updated) SELECT id,'queued',10,? FROM selected WHERE true ON CONFLICT(video_id) DO UPDATE SET state='queued',priority=10,updated=excluded.updated`, append(args, time.Now().Unix())...)
	return err
}

// MarkUploadedSelection adds one candidate while a complete refresh is pending.
func (s *Store) MarkUploadedSelection(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE videos SET selected=1 WHERE id=?`, id)
	return err
}

// AddPlaylistMember preserves existing order until full reconciliation succeeds.
func (s *Store) AddPlaylistMember(ctx context.Context, playlist, video string, position int, selected bool) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO playlist_members(playlist_id,video_id,position,selected,episode) VALUES(?,?,?,?,COALESCE((SELECT MAX(episode)+1 FROM playlist_members WHERE playlist_id=?),1)) ON CONFLICT(playlist_id,video_id) DO UPDATE SET selected=MAX(playlist_members.selected,excluded.selected)`, playlist, video, position, selected, playlist)
	return err
}

// QueueAsset schedules optional video artwork and captions outside playback work.
func (s *Store) QueueAsset(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO asset_jobs(video_id,state) VALUES(?,'queued') ON CONFLICT(video_id) DO UPDATE SET state=CASE WHEN updated<? THEN 'queued' ELSE state END`, id, time.Now().Add(-6*time.Hour).Unix())
	return err
}

// NextAsset returns one pending optional asset job.
func (s *Store) NextAsset(ctx context.Context) (string, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT video_id FROM asset_jobs WHERE state='queued' ORDER BY updated,video_id LIMIT 1`).Scan(&id)
	return id, err
}

// FinishAsset makes a source failure visible without changing media readiness.
func (s *Store) FinishAsset(ctx context.Context, id string, failure error) error {
	state, message := "ready", ""
	if failure != nil {
		state = "failed"
		message = "Optional artwork or caption retrieval failed"
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE asset_jobs SET state=?,error=?,updated=? WHERE video_id=?`, state, message, time.Now().Unix(), id)
	return err
}
