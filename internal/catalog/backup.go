package catalog

import (
	"context"
	"database/sql"
	"errors"
	"os"
)

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
