package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestProjectionStatementsRetainPlansAndReadCurrentBindings(t *testing.T) {
	s := collectionStore(t)

	const query = `SELECT title FROM channels WHERE id=?`

	for _, owner := range []string{"uploader", "owner", "missing"} {
		for _, title := range []string{"First", "Renamed"} {
			if _, err := s.DB.ExecContext(t.Context(), `UPDATE channels SET title=? WHERE id=?`, title, owner); err != nil {
				t.Fatal(err)
			}

			var got string

			err := s.scanProjectionRow(t.Context(), query, []any{owner}, &got)
			if owner == "missing" {
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatal(got, err)
				}
			} else if err != nil || got != title {
				t.Fatal("prepared query retained stale data or bindings", owner, got, title, err)
			}
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var got string
	if err := s.scanProjectionRow(ctx, query, []any{"owner"}, &got); !errors.Is(err, context.Canceled) {
		t.Fatal("cached query ignored cancellation", err)
	}

	if err := s.scanProjectionRow(ctx, "SELECT ?", []any{"owner"}, &got); !errors.Is(err, context.Canceled) {
		t.Fatal("uncached prepare ignored cancellation", err)
	}

	rows, err := s.queryProjectionRows(t.Context(), "invalid projection query")
	if rows != nil {
		defer closeRows(rows)

		if err := rows.Err(); err != nil {
			t.Error(err)
		}
	}

	if err == nil {
		t.Fatal("invalid query became a successful read")
	}

	if len(s.projections.entries) != 1 {
		t.Fatal("bindings or failed prepares consumed query cache entries", len(s.projections.entries))
	}
}

func TestProjectionStatementsStayBoundedAndCloseWithDatabase(t *testing.T) {
	s := collectionStore(t)

	for i := range projectionStatementLimit + 8 {
		var got int

		query := fmt.Sprintf("SELECT ? /* projection variant %d */", i)
		if err := s.scanProjectionRow(t.Context(), query, []any{i}, &got); err != nil || got != i {
			t.Fatal("query cache capacity changed the result", got, i, err)
		}
	}

	if len(s.projections.entries) != projectionStatementLimit {
		t.Fatal("query retention is unbounded", len(s.projections.entries))
	}

	var got int

	func() {
		rows, err := s.queryProjectionRows(t.Context(), "SELECT 42 /* uncached rows */")
		if err != nil {
			t.Fatal(err)
		}
		defer closeRows(rows)

		if !rows.Next() {
			t.Fatal(rows.Err())
		}

		if err = rows.Scan(&got); err != nil || got != 42 {
			t.Fatal(got, err)
		}

		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
	}()

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if err := s.scanProjectionRow(t.Context(), "SELECT ? /* projection variant 0 */", []any{1}, &got); err == nil {
		t.Fatal("cached statement remained usable after database close")
	}

	rows, err := s.queryProjectionRows(t.Context(), "SELECT 99 /* closed uncached rows */")
	if rows != nil {
		defer closeRows(rows)

		if err := rows.Err(); err != nil {
			t.Error(err)
		}
	}

	if err == nil {
		t.Fatal("uncached query remained usable after database close")
	}
}

func TestProjectionStatementTextBudgetDoesNotRejectLargeQueries(t *testing.T) {
	s := collectionStore(t)
	padding := strings.Repeat("x", projectionStatementSQLBytes/2-64)

	for i := range 4 {
		query := fmt.Sprintf("SELECT ? /* %d %s */", i, padding)

		var got int
		if err := s.scanProjectionRow(t.Context(), query, []any{i}, &got); err != nil || got != i {
			t.Fatal(got, err)
		}
	}

	if len(s.projections.entries) != 2 || s.projections.bytes > projectionStatementSQLBytes {
		t.Fatal("query text exceeded its retention budget", len(s.projections.entries), s.projections.bytes)
	}
}

func TestProjectionStatementsSupportConcurrentQueries(t *testing.T) {
	s := collectionStore(t)

	var group sync.WaitGroup

	for worker := range 8 {
		group.Go(func() {
			for value := range 32 {
				var got int
				if err := s.scanProjectionRow(t.Context(), "SELECT ?", []any{worker*32 + value}, &got); err != nil || got != worker*32+value {
					t.Error(got, worker, value, err)
					return
				}
			}
		})
	}

	group.Wait()
}
