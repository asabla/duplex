package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"duplex/internal/source"
)

// SaveMetadata updates canonical metadata without removing existing media.
func (s *Store) SaveMetadata(ctx context.Context, m source.Metadata) error {
	if m.ID == "" || m.ChannelID == "" {
		return errors.New("source video and uploader identities are required")
	}
	if err := s.EnsureChannel(ctx, m.ChannelID, m.Channel); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var priorOwner string
	var metadataAt int64
	err = tx.QueryRowContext(ctx, `SELECT channel_id,metadata_at FROM videos WHERE id=?`, m.ID).Scan(&priorOwner, &metadataAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if metadataAt > 0 && priorOwner != m.ChannelID {
		return errors.New("source uploader identity changed")
	}
	date := m.Date()
	published := int64(0)
	if !date.IsZero() {
		published = date.Unix()
	}
	now := time.Now().Unix()
	_, err = tx.ExecContext(ctx, `INSERT INTO videos(id,channel_id,title,search_title,description,state,published,updated,metadata_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET channel_id=excluded.channel_id,title=excluded.title,search_title=excluded.search_title,description=excluded.description,state=excluded.state,published=CASE WHEN excluded.published<>0 THEN excluded.published ELSE videos.published END,updated=excluded.updated,metadata_at=excluded.metadata_at`, m.ID, m.ChannelID, m.Title, strings.ToLower(m.Title), m.Description, m.State(), published, now, now)
	if err != nil {
		return err
	}
	// Persist catalogue metadata without signed URLs, captions, or format locators.
	m.Formats = nil
	m.Subtitles = nil
	m.AutomaticCaptions = nil
	m.Thumbnail = ""
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO metadata_revisions(video_id,revision,metadata) SELECT ?,COALESCE(MAX(revision),0)+1,? FROM metadata_revisions WHERE video_id=?`, m.ID, b, m.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SavePrepared publishes one verified representation while retaining its slot.
func (s *Store) SavePrepared(ctx context.Context, p Prepared) error {
	if p.Revision == "" || p.Size <= 0 || p.Height <= 0 {
		return errors.New("prepared media must have an exact verified representation")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var year, number int
	if err = tx.QueryRowContext(ctx, `SELECT year,number FROM videos WHERE id=?`, p.Metadata.ID).Scan(&year, &number); err != nil {
		return err
	}
	if number == 0 {
		date := p.Metadata.Date()
		if date.IsZero() {
			return errors.New("source publication date is unavailable")
		}
		year = date.Year()
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM videos WHERE channel_id=? AND year=?`, p.Metadata.ChannelID, year).Scan(&number); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO representations(revision,video_id,height,size,created) VALUES(?,?,?,?,?)`, p.Revision, p.Metadata.ID, p.Height, p.Size, time.Now().Unix())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE videos SET exposed=selected,year=?,number=?,revision=?,height=?,fallback=?,size=?,preparation='ready',preparation_error='',updated=? WHERE id=?`, year, number, p.Revision, p.Height, p.Fallback, p.Size, time.Now().Unix(), p.Metadata.ID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO preparation_jobs(video_id,state,updated) VALUES(?,'ready',?) ON CONFLICT(video_id) DO UPDATE SET state='ready',error='',updated=excluded.updated`, p.Metadata.ID, time.Now().Unix())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SetPreparation records a bounded worker's durable item state.
func (s *Store) SetPreparation(ctx context.Context, id, state, message string) error {
	switch state {
	case "queued", "resolving", "preparing", "ready", "failed", "waiting", "skipped":
	default:
		return errors.New("invalid preparation state")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	_, err = tx.ExecContext(ctx, `UPDATE videos SET preparation=?,preparation_error=? WHERE id=?`, state, message, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO preparation_jobs(video_id,state,updated,error) VALUES(?,?,?,?) ON CONFLICT(video_id) DO UPDATE SET state=excluded.state,updated=excluded.updated,error=excluded.error`, id, state, time.Now().Unix(), message)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ExposePrepared adds a verified year episode without replacing the old window.
func (s *Store) ExposePrepared(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE videos SET selected=1,exposed=1 WHERE id=? AND preparation='ready' AND revision<>'' AND number>0`, id)
	return err
}

// SelectUploads reconciles one successfully discovered eligible window.
func (s *Store) SelectUploads(ctx context.Context, channel string, selected []string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.ExecContext(ctx, `UPDATE videos SET exposed=0,selected=0 WHERE channel_id=?`, channel); err != nil {
		return err
	}
	for _, id := range selected {
		if _, err = tx.ExecContext(ctx, `UPDATE videos SET selected=1,exposed=(revision<>'' AND number>0 AND state IN ('recorded','replay-ready')) WHERE id=? AND channel_id=?`, id, channel); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE channels SET synced_at=?,error='' WHERE id=?`, time.Now().Unix(), channel); err != nil {
		return err
	}
	return tx.Commit()
}

// JobProgress updates stage counters without leaking upstream failure details.
func (s *Store) JobProgress(ctx context.Context, id int64, stage string, discovered, prepared int) error {
	if id == 0 {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE sync_jobs SET stage=?,discovered=?,prepared=? WHERE id=?`, stage, discovered, prepared, id)
	return err
}
