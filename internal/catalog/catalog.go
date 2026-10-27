// Package catalog stores source identities, selection, preparation, and Plex bindings.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // Register the pure Go SQLite database driver.
)

// Store owns serialized writes and bounded readers of committed projection state.
type Store struct {
	DB              *sql.DB
	projectionReads *sql.DB
	projections     projectionStatements
}

// Open opens and migrates a catalogue for callers without an existing context.
func Open(path string) (*Store, error) { return OpenContext(context.Background(), path) }

// OpenContext opens and migrates the catalogue with cancellation.
func OpenContext(ctx context.Context, path string) (*Store, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0o700); e != nil {
		return nil, e
	}

	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}

	db.SetMaxOpenConns(1)

	var version int
	if e = db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); e != nil || version > 9 {
		return nil, errors.Join(errors.New("unsupported catalogue schema version"), db.Close())
	}

	s := &Store{DB: db}

	_, e = db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS channels(id TEXT PRIMARY KEY,title TEXT NOT NULL,synced_at INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS videos(id TEXT PRIMARY KEY,channel_id TEXT NOT NULL REFERENCES channels(id),title TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',state TEXT NOT NULL,published INTEGER NOT NULL DEFAULT 0,year INTEGER NOT NULL DEFAULT 0,number INTEGER NOT NULL DEFAULT 0,exposed INTEGER NOT NULL DEFAULT 0,revision TEXT NOT NULL DEFAULT '',height INTEGER NOT NULL DEFAULT 0,fallback TEXT NOT NULL DEFAULT '',size INTEGER NOT NULL DEFAULT 0,updated INTEGER NOT NULL,seen INTEGER NOT NULL DEFAULT 0);
CREATE UNIQUE INDEX IF NOT EXISTS episode_slots ON videos(channel_id,year,number) WHERE number>0;
CREATE TABLE IF NOT EXISTS metadata_revisions(video_id TEXT NOT NULL REFERENCES videos(id),revision INTEGER NOT NULL,metadata BLOB NOT NULL,PRIMARY KEY(video_id,revision));
CREATE TABLE IF NOT EXISTS representations(revision TEXT PRIMARY KEY,video_id TEXT NOT NULL,height INTEGER NOT NULL,size INTEGER NOT NULL,created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS sync_jobs(id INTEGER PRIMARY KEY AUTOINCREMENT,channel_id TEXT NOT NULL,state TEXT NOT NULL,started INTEGER NOT NULL,finished INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS plex_bindings(video_id TEXT NOT NULL,server TEXT NOT NULL,rating_key TEXT NOT NULL,guid TEXT NOT NULL,PRIMARY KEY(video_id,server));
`)
	if e != nil {
		return nil, errors.Join(e, db.Close())
	}

	if e = s.migrate(ctx, version); e != nil {
		return nil, errors.Join(e, db.Close())
	}

	if _, e = db.ExecContext(ctx, `UPDATE show_metadata SET attempted=0 WHERE pending=1 AND error=''`); e != nil {
		return nil, errors.Join(e, db.Close())
	}

	if _, e = db.ExecContext(ctx, `UPDATE episode_metadata SET attempted=0 WHERE pending=1 AND error=''`); e != nil {
		return nil, errors.Join(e, db.Close())
	}

	if _, e = db.ExecContext(ctx, `UPDATE asset_jobs SET state='queued',updated=0 WHERE state='warming'`); e != nil {
		return nil, errors.Join(e, db.Close())
	}

	_, e = db.ExecContext(ctx, `UPDATE sync_jobs SET state='interrupted',finished=?,error='service restarted during sync' WHERE state='running'`, time.Now().Unix())
	if e != nil {
		return nil, errors.Join(e, db.Close())
	}

	if e = s.openProjectionReads(ctx); e != nil {
		return nil, errors.Join(e, db.Close())
	}

	return s, nil
}

// Close releases both catalogue pools after their workers have stopped.
func (s *Store) Close() error {
	var err error
	if s.projectionReads != nil {
		err = s.projectionReads.Close()
	}

	return errors.Join(err, s.DB.Close())
}
