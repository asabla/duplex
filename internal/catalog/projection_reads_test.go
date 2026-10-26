package catalog

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectionReadsUseCommittedStateDuringWrites(t *testing.T) {
	s := collectionStore(t)

	stamp, err := s.RecordShowMetadata(t.Context(), "owner", "stable", 1)
	if err != nil {
		t.Fatal(err)
	}

	for _, commit := range []bool{false, true} {
		tx, err := s.DB.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer rollback(tx)

		if _, err = tx.ExecContext(t.Context(), `UPDATE channels SET title='Changed' WHERE id='owner'`); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		title, _, _, err := s.Branding(ctx, "owner")
		if err != nil || title != "owner" {
			t.Fatal("projection blocked on writer or exposed uncommitted data", title, err)
		}

		unchanged, err := s.RecordShowMetadata(ctx, "owner", "stable", stamp+1000)
		if err != nil || unchanged != stamp {
			t.Fatal("unchanged show metadata required the writer", unchanged, err)
		}

		rows, err := s.queryProjectionRows(ctx, `SELECT title FROM channels WHERE id=?`, "owner")
		if err != nil {
			t.Fatal(err)
		}
		defer closeRows(rows)

		if !rows.Next() {
			t.Fatal("committed projection disappeared")
		}

		if err = rows.Scan(&title); err != nil || title != "owner" {
			t.Fatal(title, err)
		}

		if rows.Next() {
			t.Fatal("identity query returned multiple rows")
		}

		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}

		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}

		if err != nil {
			t.Fatal(err)
		}

		want := "owner"
		if commit {
			want = "Changed"
		}

		if title, _, _, err := s.Branding(t.Context(), "owner"); err != nil || title != want {
			t.Fatal("projection did not follow commit or rollback", title, err)
		}
	}
}

func TestProjectionReaderConnectionsAreReadOnlyAndBounded(t *testing.T) {
	s := collectionStore(t)
	if s.DB.Stats().MaxOpenConnections != 1 || s.projectionReads.Stats().MaxOpenConnections != projectionReaderLimit {
		t.Fatal("connection bounds changed")
	}

	// Hold both connections so the test checks pragmas on each separate session.
	first, err := s.projectionReads.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := first.Close(); err != nil {
			t.Error(err)
		}
	}()

	second, err := s.projectionReads.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	}()

	for _, connection := range []*sql.Conn{first, second} {
		var queryOnly, timeout int
		if err := connection.QueryRowContext(t.Context(), `PRAGMA query_only`).Scan(&queryOnly); err != nil || queryOnly != 1 {
			t.Fatal("read connection can write", queryOnly, err)
		}

		if err := connection.QueryRowContext(t.Context(), `PRAGMA busy_timeout`).Scan(&timeout); err != nil || timeout != 5000 {
			t.Fatal("read connection timeout differs", timeout, err)
		}

		if _, err := connection.ExecContext(t.Context(), `UPDATE channels SET title='Forbidden'`); err == nil {
			t.Fatal("projection reader mutated catalogue")
		}

		if _, err := connection.ExecContext(t.Context(), `PRAGMA query_only=0; UPDATE channels SET title='Forbidden'`); err == nil {
			t.Fatal("reader URI allowed writes when query_only was disabled")
		}

		if _, err := connection.ExecContext(t.Context(), `PRAGMA query_only=1`); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if connection, err := s.projectionReads.Conn(ctx); !errors.Is(err, context.Canceled) {
		if connection != nil {
			if err := connection.Close(); err != nil {
				t.Error(err)
			}
		}

		t.Fatal("reader capacity wait ignored cancellation", err)
	}
}

func TestProjectionReaderMemoryFallbackAndDurableReopen(t *testing.T) {
	for _, path := range []string{":memory:", filepath.Join(t.TempDir(), "catalogue # % space.sqlite")} {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}

		if path == ":memory:" && s.projectionReader() != s.DB {
			t.Fatal("private in-memory database got an unrelated reader")
		}

		if err = s.EnsureChannel(t.Context(), "owner", "Retained"); err != nil {
			t.Fatal(err)
		}

		if title, _, _, err := s.Branding(t.Context(), "owner"); err != nil || title != "Retained" {
			t.Fatal("reader opened a different database", title, err)
		}

		readers := s.projectionReader()
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}

		if err = readers.PingContext(t.Context()); err == nil {
			t.Fatal("store close retained reader connections")
		}

		if err = s.DB.PingContext(t.Context()); err == nil {
			t.Fatal("store close retained writer connection")
		}

		if path == ":memory:" {
			continue
		}

		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}

		if title, _, _, err := s.Branding(t.Context(), "owner"); err != nil || title != "Retained" {
			t.Fatal("reopen lost committed state", title, err)
		}

		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
