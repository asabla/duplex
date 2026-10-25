// Package catalog stores source identities, selection, preparation, and Plex bindings.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"duplex/internal/source"

	_ "modernc.org/sqlite" // Register the pure Go SQLite database driver.
)

type (
	// Store owns the serialized SQLite connection and transactional catalogue operations.
	Store struct{ DB *sql.DB }
	// Channel contains a stable identity and its last complete discovery result.
	Channel struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		SyncedAt int64  `json:"synced_at"`
		Error    string `json:"error,omitempty"`
	}
)

// Video is the canonical source record, independent of playlist occurrences and Plex keys.
type Video struct {
	ID               string `json:"id"`
	ChannelID        string `json:"channel_id"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	State            string `json:"state"`
	Published        int64  `json:"published"`
	Year             int    `json:"year"`
	Number           int    `json:"number"`
	Exposed          bool   `json:"exposed"`
	Revision         string `json:"revision"`
	Height           int    `json:"height"`
	Fallback         string `json:"fallback,omitempty"`
	Size             int64  `json:"size"`
	Updated          int64  `json:"updated"`
	Preparation      string `json:"preparation"`
	PreparationError string `json:"preparation_error,omitempty"`
	MetadataAt       int64  `json:"metadata_at"`
}

// Prepared describes an exact, verified media revision ready for publication.
type Prepared struct {
	Metadata        source.Metadata
	RequestedHeight int
	Revision        string
	Height          int
	Fallback        string
	Size            int64
}

// Job records discovery progress separately from durable media preparation tasks.
type Job struct {
	ID         int64  `json:"id"`
	ChannelID  string `json:"channel_id"`
	State      string `json:"state"`
	Started    int64  `json:"started"`
	Finished   int64  `json:"finished"`
	Error      string `json:"error,omitempty"`
	Stage      string `json:"stage,omitempty"`
	Discovered int    `json:"discovered"`
	Prepared   int    `json:"prepared"`
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
	if e = db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); e != nil || version > 5 {
		return nil, errors.Join(errors.New("unsupported catalogue schema version"), db.Close())
	}
	s := &Store{db}
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
	_, e = db.ExecContext(ctx, `UPDATE sync_jobs SET state='interrupted',finished=?,error='service restarted during sync' WHERE state='running'`, time.Now().Unix())
	if e != nil {
		return nil, errors.Join(e, db.Close())
	}
	return s, nil
}

// Close releases the catalogue connection after its workers have stopped.
func (s *Store) Close() error { return s.DB.Close() }

// OpenForBackup opens an existing catalogue without interrupting running jobs.
func OpenForBackup(path string) (*Store, error) {
	return OpenForBackupContext(context.Background(), path)
}

// OpenForBackupContext opens an existing catalogue without recovery mutations.
func OpenForBackupContext(ctx context.Context, path string) (*Store, error) {
	if _, e := os.Stat(path); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	if _, e = db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); e != nil {
		return nil, errors.Join(e, db.Close())
	}
	return &Store{DB: db}, nil
}

// EnsureChannel creates an identity and preserves its title when no new title is supplied.
func (s *Store) EnsureChannel(ctx context.Context, id, title string) error {
	_, e := s.DB.ExecContext(ctx, `INSERT INTO channels(id,title,updated) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET updated=CASE WHEN excluded.title<>'' AND channels.title<>excluded.title THEN MAX(channels.updated+1,excluded.updated,COALESCE((SELECT MAX(updated)+1 FROM videos WHERE channel_id=channels.id),0),COALESCE((SELECT MAX(MAX(m.updated,p.updated))+1 FROM playlist_members m JOIN playlists p ON p.id=m.playlist_id WHERE p.channel_id=channels.id),0)) ELSE channels.updated END,title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE channels.title END`, id, title, time.Now().Unix())
	return e
}

// StartJob creates a durable running discovery job.
func (s *Store) StartJob(ctx context.Context, id string) (int64, error) {
	r, e := s.DB.ExecContext(ctx, `INSERT INTO sync_jobs(channel_id,state,started) VALUES(?,'running',?)`, id, time.Now().Unix())
	if e != nil {
		return 0, e
	}
	return r.LastInsertId()
}

// FinishJob records a redacted result and retains the previous selection on failure.
func (s *Store) FinishJob(ctx context.Context, id int64, ch string, err error) error {
	state, msg := "complete", ""
	if err != nil {
		state = "failed"
		msg = "source or representation refresh failed; previous complete catalogue retained"
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer rollback(tx)
	if _, e = tx.ExecContext(ctx, `UPDATE sync_jobs SET state=?,finished=?,error=? WHERE id=?`, state, time.Now().Unix(), msg, id); e != nil {
		return e
	}
	if err != nil {
		if _, e = tx.ExecContext(ctx, `UPDATE channels SET error=? WHERE id=?`, msg, ch); e != nil {
			return e
		}
	}
	// Durable selections and preparation jobs are the restart checkpoint. Flat
	// enumeration is repeated after interruption, so completed staging is unused.
	if _, e = tx.ExecContext(ctx, `DELETE FROM discovery_items WHERE run_id=?`, id); e != nil {
		return e
	}
	return tx.Commit()
}

// Channels returns known source channels, including uploaders of curated entries.
func (s *Store) Channels(ctx context.Context) ([]Channel, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT id,title,synced_at,error FROM channels ORDER BY title,id`)
	if e != nil {
		return nil, e
	}
	return readChannels(rows)
}

// Jobs returns the latest 100 jobs for API compatibility; FilterJobs reads bounded history.
func (s *Store) Jobs(ctx context.Context) ([]Job, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT id,channel_id,state,started,finished,error,stage,discovered,prepared FROM sync_jobs ORDER BY id DESC LIMIT 100`)
	if e != nil {
		return nil, e
	}
	return readJobs(rows)
}

func readJobs(rows *sql.Rows) ([]Job, error) {
	defer closeRows(rows)
	out := []Job{}
	for rows.Next() {
		var j Job
		if e := rows.Scan(&j.ID, &j.ChannelID, &j.State, &j.Started, &j.Finished, &j.Error, &j.Stage, &j.Discovered, &j.Prepared); e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// JobsPage reads a bounded page and its count from the same SQLite snapshot.
// Jobs remains the latest-100 compatibility view for existing API consumers.
func (s *Store) JobsPage(ctx context.Context, number, size int) ([]Job, int, error) {
	size = max(1, min(size, 100))
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer rollback(tx)
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_jobs`).Scan(&total); err != nil {
		return nil, 0, err
	}
	number = max(1, min(number, max(1, (total+size-1)/size)))
	rows, err := tx.QueryContext(ctx, `SELECT id,channel_id,state,started,finished,error,stage,discovered,prepared FROM sync_jobs ORDER BY id DESC LIMIT ? OFFSET ?`, size, (number-1)*size)
	if err != nil {
		return nil, 0, err
	}
	jobs, err := readJobs(rows)
	if err != nil {
		return nil, 0, err
	}
	return jobs, total, tx.Commit()
}

const videoColumns = `id,channel_id,title,description,state,published,year,number,exposed,revision,height,fallback,size,updated,preparation,preparation_error,metadata_at`

// Videos returns the complete canonical catalogue for the legacy status response.
func (s *Store) Videos(ctx context.Context) ([]Video, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT `+videoColumns+` FROM videos ORDER BY published DESC,id`)
	if e != nil {
		return nil, e
	}
	return readVideos(rows)
}

// Bind retains a year episode Plex key independently of its source identity.
func (s *Store) Bind(ctx context.Context, id, server, key, guid string) error {
	_, e := s.DB.ExecContext(ctx, `INSERT INTO plex_bindings VALUES(?,?,?,?) ON CONFLICT(video_id,server) DO UPDATE SET rating_key=excluded.rating_key,guid=excluded.guid`, id, server, key, guid)
	return e
}

// Backup creates a new, private SQLite snapshot without stopping the service.
func (s *Store) Backup(ctx context.Context, path string) error {
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		return errors.New("backup destination must not exist")
	}
	_, e := s.DB.ExecContext(ctx, `VACUUM INTO ?`, path)
	if e == nil {
		e = os.Chmod(path, 0o600)
	}
	return e
}
