package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"duplex/internal/source"
	_ "modernc.org/sqlite"
)

type Store struct{ DB *sql.DB }
type Channel struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	SyncedAt int64  `json:"synced_at"`
	Error    string `json:"error,omitempty"`
}
type Video struct {
	ID          string `json:"id"`
	ChannelID   string `json:"channel_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	Published   int64  `json:"published"`
	Year        int    `json:"year"`
	Number      int    `json:"number"`
	Exposed     bool   `json:"exposed"`
	Revision    string `json:"revision"`
	Height      int    `json:"height"`
	Fallback    string `json:"fallback,omitempty"`
	Size        int64  `json:"size"`
	Updated     int64  `json:"updated"`
}
type Prepared struct {
	Metadata source.Metadata
	Revision string
	Height   int
	Fallback string
	Size     int64
}
type Job struct {
	ID        int64  `json:"id"`
	ChannelID string `json:"channel_id"`
	State     string `json:"state"`
	Started   int64  `json:"started"`
	Finished  int64  `json:"finished"`
	Error     string `json:"error,omitempty"`
}

func Open(path string) (*Store, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	var version int
	if e = db.QueryRow(`PRAGMA user_version`).Scan(&version); e != nil || version > 1 {
		db.Close()
		return nil, errors.New("unsupported catalogue schema version")
	}
	s := &Store{db}
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS channels(id TEXT PRIMARY KEY,title TEXT NOT NULL,synced_at INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS videos(id TEXT PRIMARY KEY,channel_id TEXT NOT NULL REFERENCES channels(id),title TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',state TEXT NOT NULL,published INTEGER NOT NULL DEFAULT 0,year INTEGER NOT NULL DEFAULT 0,number INTEGER NOT NULL DEFAULT 0,exposed INTEGER NOT NULL DEFAULT 0,revision TEXT NOT NULL DEFAULT '',height INTEGER NOT NULL DEFAULT 0,fallback TEXT NOT NULL DEFAULT '',size INTEGER NOT NULL DEFAULT 0,updated INTEGER NOT NULL,seen INTEGER NOT NULL DEFAULT 0);
CREATE UNIQUE INDEX IF NOT EXISTS episode_slots ON videos(channel_id,year,number) WHERE number>0;
CREATE TABLE IF NOT EXISTS metadata_revisions(video_id TEXT NOT NULL REFERENCES videos(id),revision INTEGER NOT NULL,metadata BLOB NOT NULL,PRIMARY KEY(video_id,revision));
CREATE TABLE IF NOT EXISTS representations(revision TEXT PRIMARY KEY,video_id TEXT NOT NULL,height INTEGER NOT NULL,size INTEGER NOT NULL,created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS sync_jobs(id INTEGER PRIMARY KEY AUTOINCREMENT,channel_id TEXT NOT NULL,state TEXT NOT NULL,started INTEGER NOT NULL,finished INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS plex_bindings(video_id TEXT NOT NULL,server TEXT NOT NULL,rating_key TEXT NOT NULL,guid TEXT NOT NULL,PRIMARY KEY(video_id,server));
PRAGMA user_version=1;`)
	if e != nil {
		db.Close()
		return nil, e
	}
	_, e = db.Exec(`UPDATE sync_jobs SET state='interrupted',finished=?,error='service restarted during sync' WHERE state='running'`, time.Now().Unix())
	if e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func OpenForBackup(path string) (*Store, error) {
	if _, e := os.Stat(path); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	if _, e = db.Exec(`PRAGMA busy_timeout=5000`); e != nil {
		db.Close()
		return nil, e
	}
	return &Store{DB: db}, nil
}
func (s *Store) EnsureChannel(ctx context.Context, id, title string) error {
	_, e := s.DB.ExecContext(ctx, `INSERT INTO channels(id,title) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title`, id, title)
	return e
}
func (s *Store) StartJob(ctx context.Context, id string) (int64, error) {
	r, e := s.DB.ExecContext(ctx, `INSERT INTO sync_jobs(channel_id,state,started) VALUES(?,'running',?)`, id, time.Now().Unix())
	if e != nil {
		return 0, e
	}
	return r.LastInsertId()
}
func (s *Store) FinishJob(ctx context.Context, id int64, ch string, err error) error {
	state, msg := "complete", ""
	if err != nil {
		state = "failed"
		msg = "source or representation refresh failed; previous complete catalogue retained"
	}
	_, e := s.DB.ExecContext(ctx, `UPDATE sync_jobs SET state=?,finished=?,error=? WHERE id=?`, state, time.Now().Unix(), msg, id)
	if e == nil && err != nil {
		_, e = s.DB.ExecContext(ctx, `UPDATE channels SET error=? WHERE id=?`, msg, ch)
	}
	return e
}
func (s *Store) Channels(ctx context.Context) ([]Channel, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT id,title,synced_at,error FROM channels ORDER BY title,id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var v Channel
		if e = rows.Scan(&v.ID, &v.Title, &v.SyncedAt, &v.Error); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Jobs(ctx context.Context) ([]Job, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT id,channel_id,state,started,finished,error FROM sync_jobs ORDER BY id DESC LIMIT 100`)
	if e != nil {
		return nil, e
	}
	return readJobs(rows)
}

func readJobs(rows *sql.Rows) ([]Job, error) {
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var j Job
		if e := rows.Scan(&j.ID, &j.ChannelID, &j.State, &j.Started, &j.Finished, &j.Error); e != nil {
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
	defer tx.Rollback()
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_jobs`).Scan(&total); err != nil {
		return nil, 0, err
	}
	number = max(1, min(number, max(1, (total+size-1)/size)))
	rows, err := tx.QueryContext(ctx, `SELECT id,channel_id,state,started,finished,error FROM sync_jobs ORDER BY id DESC LIMIT ? OFFSET ?`, size, (number-1)*size)
	if err != nil {
		return nil, 0, err
	}
	jobs, err := readJobs(rows)
	if err != nil {
		return nil, 0, err
	}
	return jobs, total, tx.Commit()
}

const videoColumns = `id,channel_id,title,description,state,published,year,number,exposed,revision,height,fallback,size,updated`

func (s *Store) Videos(ctx context.Context) ([]Video, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT `+videoColumns+` FROM videos ORDER BY published DESC,id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Video{}
	for rows.Next() {
		var v Video
		if e = rows.Scan(&v.ID, &v.ChannelID, &v.Title, &v.Description, &v.State, &v.Published, &v.Year, &v.Number, &v.Exposed, &v.Revision, &v.Height, &v.Fallback, &v.Size, &v.Updated); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CommitSnapshot is the sole reconciliation boundary. Enumerators must return a
// complete snapshot; a caller must never call this with partial source output.
// Identity records and previously assigned episode slots are never deleted.
func (s *Store) CommitSnapshot(ctx context.Context, ch source.Channel, prepared []Prepared, limit int) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, e = tx.ExecContext(ctx, `INSERT INTO channels(id,title) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title`, ch.ID, ch.Title); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE videos SET seen=0,exposed=0 WHERE channel_id=?`, ch.ID); e != nil {
		return e
	}
	for _, entry := range ch.Entries {
		if _, e = tx.ExecContext(ctx, `INSERT INTO videos(id,channel_id,title,state,updated,seen) VALUES(?,?,?,'unavailable',?,1) ON CONFLICT(id) DO UPDATE SET seen=1`, entry.ID, ch.ID, entry.Title, now); e != nil {
			return e
		}
	}
	verified := []string{}
	for _, p := range prepared {
		m := p.Metadata
		if m.ChannelID != ch.ID {
			return errors.New("metadata channel identity changed")
		}
		date := m.Date()
		year, published := 0, int64(0)
		if !date.IsZero() {
			year, published = date.Year(), date.Unix()
		}
		var assignedYear, number int
		var priorPublished int64
		if e = tx.QueryRowContext(ctx, `SELECT year,number,published FROM videos WHERE id=? AND channel_id=? AND seen=1`, m.ID, ch.ID).Scan(&assignedYear, &number, &priorPublished); e != nil {
			return e
		}
		if number > 0 {
			year = assignedYear
			if published == 0 {
				published = priorPublished
			}
		} else if p.Revision != "" && year > 0 {
			if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM videos WHERE channel_id=? AND year=?`, ch.ID, year).Scan(&number); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, `UPDATE videos SET title=?,description=?,state=?,published=?,year=?,number=?,revision=?,height=?,fallback=?,size=?,updated=? WHERE id=?`, m.Title, m.Description, m.State(), published, year, number, p.Revision, p.Height, p.Fallback, p.Size, now, m.ID); e != nil {
			return e
		}
		// Keep durable metadata without signed locators, challenge data, or credentials.
		clean := struct {
			Title, Description, State, UploadDate, Language string
			Duration                                        float64
		}{m.Title, m.Description, m.State(), m.UploadDate, m.Language, m.Duration}
		b, _ := json.Marshal(clean)
		if _, e = tx.ExecContext(ctx, `INSERT INTO metadata_revisions(video_id,revision,metadata) VALUES(?,COALESCE((SELECT MAX(revision)+1 FROM metadata_revisions WHERE video_id=?),1),?)`, m.ID, m.ID, b); e != nil {
			return e
		}
		if p.Revision != "" {
			verified = append(verified, m.ID)
			if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO representations(revision,video_id,height,size,created) VALUES(?,?,?,?,?)`, p.Revision, m.ID, p.Height, p.Size, now); e != nil {
				return e
			}
		}
	}
	if len(verified) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(verified)), ",")
		args := []any{ch.ID}
		for _, id := range verified {
			args = append(args, id)
		}
		args = append(args, limit)
		query := `UPDATE videos SET exposed=1 WHERE id IN (SELECT id FROM videos WHERE channel_id=? AND seen=1 AND revision<>'' AND number>0 AND state IN ('recorded','replay-ready') AND id IN (` + placeholders + `) ORDER BY published DESC,id LIMIT ?)`
		if _, e = tx.ExecContext(ctx, query, args...); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE videos SET state='unavailable' WHERE channel_id=? AND seen=0`, ch.ID); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE channels SET title=?,synced_at=?,error='' WHERE id=?`, ch.Title, now, ch.ID); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Bind(ctx context.Context, id, server, key, guid string) error {
	_, e := s.DB.ExecContext(ctx, `INSERT INTO plex_bindings VALUES(?,?,?,?) ON CONFLICT(video_id,server) DO UPDATE SET rating_key=excluded.rating_key,guid=excluded.guid`, id, server, key, guid)
	return e
}
func (s *Store) Backup(ctx context.Context, path string) error {
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		return errors.New("backup destination must not exist")
	}
	_, e := s.DB.ExecContext(ctx, `VACUUM INTO ?`, path)
	if e == nil {
		e = os.Chmod(path, 0600)
	}
	return e
}
