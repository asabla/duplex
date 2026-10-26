package catalog

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

const (
	projectionStatementLimit    = 32
	projectionStatementSQLBytes = 128 << 10
)

// projectionStatements retains query plans, never query results or bindings.
// database/sql owns statement lifetime and closes driver resources with DB.
// The mutex protects retention only; preparation and reads hold no cache lock.
type projectionStatements struct {
	mu      sync.Mutex
	entries map[string]*sql.Stmt
	bytes   int
}

func (s *Store) projectionStatement(ctx context.Context, query string) (statement *sql.Stmt, result error) {
	cache := &s.projections
	cache.mu.Lock()
	statement = cache.entries[query]
	room := len(cache.entries) < projectionStatementLimit && cache.bytes+len(query) <= projectionStatementSQLBytes
	cache.mu.Unlock()

	if statement != nil || !room {
		return statement, nil
	}

	prepared, err := s.DB.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}

	retained := false
	defer func() {
		if !retained {
			result = errors.Join(result, prepared.Close())
		}
	}()

	cache.mu.Lock()

	statement = cache.entries[query]
	if statement == nil && len(cache.entries) < projectionStatementLimit && cache.bytes+len(query) <= projectionStatementSQLBytes {
		if cache.entries == nil {
			cache.entries = map[string]*sql.Stmt{}
		}

		cache.entries[query] = prepared
		cache.bytes += len(query)
		retained = true
		cache.mu.Unlock()

		return prepared, nil
	}

	cache.mu.Unlock()

	return statement, nil
}

func (s *Store) queryProjectionRows(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	statement, err := s.projectionStatement(ctx, query) //nolint:sqlclosecheck // Store retains shared statements; the database closes their driver resources.
	if err != nil {
		return nil, err
	}

	if statement == nil {
		return s.DB.QueryContext(ctx, query, args...)
	}

	return statement.QueryContext(ctx, args...)
}

func (s *Store) scanProjectionRow(ctx context.Context, query string, args []any, targets ...any) error {
	statement, err := s.projectionStatement(ctx, query) //nolint:sqlclosecheck // Store retains shared statements; the database closes their driver resources.
	if err != nil {
		return err
	}

	if statement == nil {
		return s.DB.QueryRowContext(ctx, query, args...).Scan(targets...)
	}

	return statement.QueryRowContext(ctx, args...).Scan(targets...)
}
