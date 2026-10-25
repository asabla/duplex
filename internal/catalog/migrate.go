package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func (s *Store) migrate(ctx context.Context, version int) error {
	if version == 2 {
		return nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	statements := []string{
		`ALTER TABLE videos ADD COLUMN search_title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE videos ADD COLUMN selected INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE videos ADD COLUMN preparation TEXT NOT NULL DEFAULT 'discovered'`,
		`ALTER TABLE videos ADD COLUMN preparation_error TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE videos ADD COLUMN metadata_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE channels ADD COLUMN avatar TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE channels ADD COLUMN banner TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sync_jobs ADD COLUMN stage TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sync_jobs ADD COLUMN discovered INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sync_jobs ADD COLUMN prepared INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE playlists(id TEXT PRIMARY KEY,channel_id TEXT NOT NULL REFERENCES channels(id),title TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',thumbnail TEXT NOT NULL DEFAULT '',season INTEGER NOT NULL,synced_at INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',UNIQUE(channel_id,season))`,
		`CREATE TABLE playlist_members(playlist_id TEXT NOT NULL REFERENCES playlists(id),video_id TEXT NOT NULL REFERENCES videos(id),position INTEGER NOT NULL,selected INTEGER NOT NULL DEFAULT 0,episode INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(playlist_id,video_id))`,
		`CREATE TABLE discovery_items(run_id INTEGER NOT NULL,collection TEXT NOT NULL,video_id TEXT NOT NULL REFERENCES videos(id),position INTEGER NOT NULL,chosen INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(run_id,collection,video_id))`,
		`CREATE TABLE preparation_jobs(video_id TEXT PRIMARY KEY REFERENCES videos(id),state TEXT NOT NULL DEFAULT 'queued',priority INTEGER NOT NULL DEFAULT 0,updated INTEGER NOT NULL,error TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE episode_bindings(occurrence_id TEXT NOT NULL,server TEXT NOT NULL,rating_key TEXT NOT NULL,guid TEXT NOT NULL,PRIMARY KEY(occurrence_id,server))`,
		`CREATE TABLE asset_jobs(video_id TEXT PRIMARY KEY REFERENCES videos(id),state TEXT NOT NULL DEFAULT 'queued',updated INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX video_date ON videos(published DESC,id)`,
		`CREATE INDEX video_channel_date ON videos(channel_id,published DESC,id)`,
		`CREATE INDEX video_preparation_date ON videos(preparation,published DESC,id)`,
		`CREATE INDEX playlist_channel ON playlists(channel_id,title,id)`,
		`CREATE INDEX members_selected ON playlist_members(playlist_id,selected,episode)`,
		`UPDATE videos SET preparation=CASE WHEN revision<>'' THEN 'ready' ELSE 'discovered' END`,
		`UPDATE videos SET selected=exposed`,
		`PRAGMA user_version=2`,
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	if err = backfillSearch(ctx, tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func backfillSearch(ctx context.Context, tx *sql.Tx) error {
	last := ""
	for {
		rows, err := tx.QueryContext(ctx, `SELECT id,title FROM videos WHERE id>? ORDER BY id LIMIT 250`, last)
		if err != nil {
			return err
		}
		type title struct{ id, text string }
		batch := []title{}
		for rows.Next() {
			var t title
			if err = rows.Scan(&t.id, &t.text); err != nil {
				closeRows(rows)
				return err
			}
			batch = append(batch, t)
		}
		if err = rows.Err(); err != nil {
			closeRows(rows)
			return err
		}
		closeRows(rows)
		for _, t := range batch {
			if _, err = tx.ExecContext(ctx, `UPDATE videos SET search_title=? WHERE id=?`, strings.ToLower(t.text), t.id); err != nil {
				return err
			}
		}
		if len(batch) < 250 {
			return nil
		}
		last = batch[len(batch)-1].id
	}
}
