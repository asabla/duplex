package catalog

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
)

const projectionReaderLimit = 2

// Open only after migrations and restart recovery. Read connections cannot
// mutate the catalogue; every connection gets the same URI pragmas.
func (s *Store) openProjectionReads(ctx context.Context) error {
	var filename string
	if err := s.DB.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&filename); err != nil {
		return err
	}

	// Private in-memory databases cannot be reopened as the same database.
	if filename == "" {
		return nil
	}

	options := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)", "query_only(1)"}}
	location := url.URL{Scheme: "file", Path: filepath.ToSlash(filename), RawQuery: options.Encode()}

	readers, err := sql.Open("sqlite", location.String())
	if err != nil {
		return err
	}

	readers.SetMaxOpenConns(projectionReaderLimit)
	readers.SetMaxIdleConns(projectionReaderLimit)

	if err = readers.PingContext(ctx); err != nil {
		return errors.Join(err, readers.Close())
	}

	s.projectionReads = readers

	return nil
}

func (s *Store) projectionReader() *sql.DB {
	if s.projectionReads != nil {
		return s.projectionReads
	}

	return s.DB
}
