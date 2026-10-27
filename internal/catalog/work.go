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
	task, err := s.ClaimPreparationTask(ctx)
	return task.VideoID, err
}

// PreparationTask contains a durable queue item and its current request priority.
type PreparationTask struct {
	VideoID  string
	Priority int
	QueuedAt int64
}

// ClaimPreparationTask reserves the highest-priority queued item transactionally.
func (s *Store) ClaimPreparationTask(ctx context.Context) (PreparationTask, error) {
	var task PreparationTask

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return task, err
	}
	defer rollback(tx)

	if err = tx.QueryRowContext(ctx, `SELECT video_id,priority,updated FROM preparation_jobs WHERE state='queued' ORDER BY priority DESC,updated,video_id LIMIT 1`).Scan(&task.VideoID, &task.Priority, &task.QueuedAt); err != nil {
		return task, err
	}

	if _, err = tx.ExecContext(ctx, `UPDATE preparation_jobs SET state='resolving' WHERE video_id=?`, task.VideoID); err != nil {
		return task, err
	}

	return task, tx.Commit()
}

// RequestedPreparations counts urgent queued work without loading item bodies.
func (s *Store) RequestedPreparations(ctx context.Context) (int, error) {
	var count int

	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM preparation_jobs WHERE state='queued' AND priority>=1000`).Scan(&count)

	return count, err
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

// ResetPreparations resumes interrupted work and warms at most four recent,
// selected verified recordings. Other retained indexes still bind on demand.
func (s *Store) ResetPreparations(ctx context.Context, v Visibility) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	cte, args := selectedCTE(v)
	for _, query := range []string{
		`INSERT INTO preparation_jobs(video_id,state,priority,updated) SELECT v.id,'queued',10,? FROM selected s JOIN videos v ON v.id=s.id WHERE v.revision='' OR v.id IN (SELECT video_id FROM preparation_jobs WHERE state IN ('queued','resolving','preparing')) ON CONFLICT(video_id) DO UPDATE SET state='queued',priority=MAX(preparation_jobs.priority,excluded.priority),updated=excluded.updated`,
		`INSERT INTO preparation_jobs(video_id,state,priority,updated) SELECT v.id,'queued',100,? FROM selected s JOIN videos v ON v.id=s.id WHERE v.revision<>'' AND v.number>0 AND v.state IN ('recorded','replay-ready') AND v.preparation='ready' ORDER BY v.published DESC,v.id LIMIT 4 ON CONFLICT(video_id) DO UPDATE SET state='queued',priority=MAX(preparation_jobs.priority,excluded.priority),updated=excluded.updated`,
	} {
		if _, err = tx.ExecContext(ctx, cte+query, append(args, time.Now().Unix())...); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// MarkUploadedSelection adds one candidate while a complete refresh is pending.
func (s *Store) MarkUploadedSelection(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE videos SET selected=1 WHERE id=?`, id)
	return err
}

// AddPlaylistMember preserves existing order until full reconciliation succeeds.
func (s *Store) AddPlaylistMember(ctx context.Context, playlist, video string, position int, selected bool) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO playlist_members(playlist_id,video_id,position,selected,episode,file_episode,updated) VALUES(?,?,?,?,COALESCE((SELECT MAX(episode)+1 FROM playlist_members WHERE playlist_id=?),1),COALESCE((SELECT MAX(file_episode)+1 FROM playlist_members WHERE playlist_id=?),1),?) ON CONFLICT(playlist_id,video_id) DO UPDATE SET episode=CASE WHEN playlist_members.selected=0 AND excluded.selected=1 THEN excluded.episode ELSE playlist_members.episode END,updated=CASE WHEN playlist_members.selected=0 AND excluded.selected=1 THEN MAX(playlist_members.updated+1,excluded.updated) ELSE playlist_members.updated END,selected=MAX(playlist_members.selected,excluded.selected)`, playlist, video, position, selected, playlist, playlist, time.Now().Unix())
	return err
}

// QueueAsset schedules optional video artwork and captions outside playback work.
func (s *Store) QueueAsset(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO asset_jobs(video_id,state) VALUES(?,'queued') ON CONFLICT(video_id) DO UPDATE SET state=CASE WHEN updated<? THEN 'queued' ELSE state END`, id, time.Now().Add(-6*time.Hour).Unix())
	return err
}

// BeginInitialAsset reserves the first local artifact pass before the background
// worker can claim it. Existing refresh jobs keep their current owner and state.
func (s *Store) BeginInitialAsset(ctx context.Context, id string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `INSERT INTO asset_jobs(video_id,state,updated) VALUES(?,'warming',?) ON CONFLICT(video_id) DO NOTHING`, id, time.Now().Unix())
	if err != nil {
		return false, err
	}

	count, err := result.RowsAffected()

	return count == 1, err
}

// RetryInitialAsset leaves interrupted bounded work for the normal worker.
func (s *Store) RetryInitialAsset(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE asset_jobs SET state='queued',updated=0,error='' WHERE video_id=? AND state='warming'`, id)
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

// PreparedForHeight allows durable verified entries to remain unloaded until use.
func (s *Store) PreparedForHeight(ctx context.Context, id string, height int) (bool, error) {
	var ready bool

	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM videos WHERE id=? AND revision<>'' AND requested_height=? AND preparation='ready' AND state IN ('recorded','replay-ready'))`, id, height).Scan(&ready)

	return ready, err
}
