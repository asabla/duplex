package nfs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"path"
	"sync"

	"duplex/internal/library"

	_ "modernc.org/sqlite" // NFS wire identities are separate from catalogue state.
)

type record struct {
	id   []byte
	name string
	info library.Node
}

type handles struct {
	db   *sql.DB
	seed []byte
	mu   sync.Mutex
}

func openHandles(ctx context.Context, filename string) (*handles, error) {
	// The application supplies its private data directory, not an NFS pathname.
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // Fixed database name under the configured private data directory.
	if err != nil {
		return nil, err
	}

	if err = file.Close(); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", filename)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	h := &handles{db: db}
	if err = h.initialize(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return h, nil
}

func (h *handles) initialize(ctx context.Context) error {
	_, err := h.db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS identity(id INTEGER PRIMARY KEY CHECK(id=1), seed BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS handles(id BLOB PRIMARY KEY, parent BLOB NOT NULL, cookie INTEGER NOT NULL, name TEXT NOT NULL, directory INTEGER NOT NULL, revision TEXT NOT NULL, size INTEGER NOT NULL, modified INTEGER NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS handle_cookies ON handles(parent,cookie);`)
	if err != nil {
		return err
	}

	seed := make([]byte, 32)
	if _, err = rand.Read(seed); err != nil {
		return err
	}

	if _, err = h.db.ExecContext(ctx, `INSERT OR IGNORE INTO identity VALUES(1,?)`, seed); err != nil {
		return err
	}

	return h.db.QueryRowContext(ctx, `SELECT seed FROM identity WHERE id=1`).Scan(&h.seed)
}

func (h *handles) describe(name string, info library.Node) record {
	revision := info.Revision
	if info.Directory {
		revision = ""
	}

	identity := sha256.New()
	identity.Write(h.seed)
	identity.Write([]byte(name + "\x00" + revision))

	return record{id: identity.Sum(nil), name: name, info: info}
}

// A membership change must advance mtime even within the same clock second.
// Persist the logical timestamp without changing the directory's file handle.
func (h *handles) directoryTime(ctx context.Context, r record) (record, error) {
	if !r.info.Directory || r.info.Revision == "" {
		return r, nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	var (
		revision string
		modified int64
	)

	err := h.db.QueryRowContext(ctx, `SELECT revision,modified FROM handles WHERE id=?`, r.id).Scan(&revision, &modified)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}

	if err != nil {
		return r, err
	}

	r.info.Modified = max(r.info.Modified, modified)
	if revision != r.info.Revision {
		r.info.Modified = max(r.info.Modified, modified+1)
	}

	if revision != r.info.Revision || modified != r.info.Modified {
		_, err = h.db.ExecContext(ctx, `UPDATE handles SET revision=?,modified=? WHERE id=?`, r.info.Revision, r.info.Modified, r.id)
	}

	return r, err
}

func cookie(id []byte) uint64 { return binary.BigEndian.Uint64(id[:8])&0x7fffffffffffffff | 2 }

func (h *handles) save(ctx context.Context, records []record) (result error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			result = errors.Join(result, err)
		}
	}()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO handles VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, stmt.Close()) }()

	for _, r := range records {
		parent := path.Dir(r.name)
		if parent == "." {
			parent = ""
		}

		parentID := h.describe(parent, library.Node{Directory: true}).id
		if _, err = stmt.ExecContext(ctx, r.id, parentID, cookie(r.id), r.name, r.info.Directory, r.info.Revision, r.info.Size, r.info.Modified); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (h *handles) load(ctx context.Context, id []byte) (record, error) {
	r := record{id: id}
	if len(id) != sha256.Size {
		return r, os.ErrNotExist
	}

	err := h.db.QueryRowContext(ctx, `SELECT name,directory,revision,size,modified FROM handles WHERE id=?`, id).
		Scan(&r.name, &r.info.Directory, &r.info.Revision, &r.info.Size, &r.info.Modified)

	return r, err
}

func (h *handles) after(ctx context.Context, parent []byte, cursor uint64) (string, error) {
	var name string

	err := h.db.QueryRowContext(ctx, `SELECT name FROM handles WHERE parent=? AND cookie=?`, parent, cursor).Scan(&name)

	return path.Base(name), err
}
