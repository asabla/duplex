package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func (s *Store) migrate(ctx context.Context, version int) error {
	if version < 2 {
		if err := s.migrateCollections(ctx); err != nil {
			return err
		}
	}
	if version < 3 {
		if err := s.migrateProjection(ctx); err != nil {
			return err
		}
	}
	if version < 4 {
		if err := s.migratePlaylistRoots(ctx); err != nil {
			return err
		}
	}
	if version < 5 {
		return s.migratePlaylistPaths(ctx)
	}
	return nil
}

func (s *Store) migratePlaylistPaths(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, statement := range []string{
		`ALTER TABLE playlist_members ADD COLUMN file_episode INTEGER NOT NULL DEFAULT 0`,
		`UPDATE playlist_members SET file_episode=episode WHERE selected=1 AND episode>0`,
		`WITH pending AS MATERIALIZED (SELECT playlist_id,video_id,ROW_NUMBER() OVER(PARTITION BY playlist_id ORDER BY episode,position,video_id) AS slot FROM playlist_members WHERE file_episode=0), assigned AS MATERIALIZED (SELECT playlist_id,MAX(file_episode) AS last FROM playlist_members GROUP BY playlist_id) UPDATE playlist_members AS m SET file_episode=COALESCE((SELECT last FROM assigned WHERE playlist_id=m.playlist_id),0)+(SELECT slot FROM pending WHERE playlist_id=m.playlist_id AND video_id=m.video_id) WHERE file_episode=0`,
		`CREATE UNIQUE INDEX playlist_file_slots ON playlist_members(playlist_id,file_episode)`,
		`CREATE INDEX playlist_episode_numbers ON playlist_members(playlist_id,episode)`,
		`CREATE INDEX playlist_video_membership ON playlist_members(video_id,playlist_id)`,
		`ALTER TABLE playlists ADD COLUMN projection_revision INTEGER NOT NULL DEFAULT 1`,
		`CREATE TABLE catalogue_identity(id INTEGER PRIMARY KEY CHECK(id=1),identity TEXT NOT NULL UNIQUE)`,
		`INSERT INTO catalogue_identity VALUES(1,lower(hex(randomblob(16))))`,
		`CREATE TRIGGER playlist_hint_insert AFTER INSERT ON playlist_members BEGIN UPDATE playlists SET projection_revision=projection_revision+1 WHERE id=NEW.playlist_id; END`,
		`CREATE TRIGGER playlist_hint_update AFTER UPDATE OF selected,episode,file_episode ON playlist_members WHEN OLD.selected<>NEW.selected OR OLD.episode<>NEW.episode OR OLD.file_episode<>NEW.file_episode BEGIN UPDATE playlists SET projection_revision=projection_revision+1 WHERE id=NEW.playlist_id; END`,
		`CREATE TRIGGER playlist_hint_delete AFTER DELETE ON playlist_members BEGIN UPDATE playlists SET projection_revision=projection_revision+1 WHERE id=OLD.playlist_id; END`,
		`CREATE TRIGGER playlist_hint_media AFTER UPDATE OF revision,state ON videos WHEN ((OLD.revision<>'' AND OLD.state IN ('recorded','replay-ready'))<>(NEW.revision<>'' AND NEW.state IN ('recorded','replay-ready'))) BEGIN UPDATE playlists SET projection_revision=projection_revision+1 WHERE id IN (SELECT playlist_id FROM playlist_members WHERE video_id=NEW.id); END`,
		`CREATE TABLE show_metadata(channel_id TEXT PRIMARY KEY REFERENCES channels(id),digest TEXT NOT NULL,modified INTEGER NOT NULL,pending INTEGER NOT NULL DEFAULT 1,attempted INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '')`,
		`ALTER TABLE episode_bindings ADD COLUMN owner_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE episode_bindings ADD COLUMN playlist_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE episode_bindings ADD COLUMN video_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE episode_bindings ADD COLUMN current_index INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE episode_bindings ADD COLUMN seen_run INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX observed_episodes ON episode_bindings(server,seen_run,occurrence_id)`,
		`PRAGMA user_version=5`,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) migrateCollections(ctx context.Context) error {
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

func (s *Store) migrateProjection(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	statements := []string{
		`ALTER TABLE videos ADD COLUMN requested_height INTEGER NOT NULL DEFAULT 0`,
		`UPDATE videos SET requested_height=2160 WHERE revision<>''`,
		`ALTER TABLE playlist_members ADD COLUMN updated INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE playlists ADD COLUMN updated INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE channels ADD COLUMN updated INTEGER NOT NULL DEFAULT 0`,
		`UPDATE playlist_members SET updated=COALESCE((SELECT updated FROM videos WHERE id=video_id),0)`,
		`UPDATE playlists SET updated=synced_at`,
		`UPDATE channels SET updated=COALESCE((SELECT MAX(updated) FROM videos WHERE channel_id=channels.id),0)`,
		`CREATE INDEX video_projection ON videos(channel_id,year,number) WHERE selected=1 AND revision<>''`,
		`DELETE FROM discovery_items WHERE run_id IN (SELECT id FROM sync_jobs WHERE state<>'running')`,
		`PRAGMA user_version=3`,
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) migratePlaylistRoots(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, statement := range []string{`ALTER TABLE playlists ADD COLUMN seen_run INTEGER NOT NULL DEFAULT 0`, `ALTER TABLE playlists ADD COLUMN available INTEGER NOT NULL DEFAULT 1`, `PRAGMA user_version=4`} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}
