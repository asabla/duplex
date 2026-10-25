// Package cache implements a bounded, crash-recoverable source-chunk cache.
package cache

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

// ErrFull means reservations cannot be satisfied without evicting pinned data.
var ErrFull = errors.New("media cache full: all reclaimable space is in use")

// ChunkStore reserves space before fetch, deduplicates concurrent fetches,
// publishes atomically, and pins the returned bytes until release. Eviction is
// permitted only for unpinned published chunks.
type ChunkStore interface {
	Get(context.Context, string, int64, int, func(context.Context) ([]byte, error)) ([]byte, func(), error)
	Stats() Stats
	Resize(int64) error
	Close() error
}

type entry struct {
	path       string
	digest     string
	allocation int64
	pins       int
	used       time.Time
	done       chan struct{}
	err        error
}

// Store accounts for published chunks, active pins, and unfinished write reservations.
type Store struct {
	mu          sync.Mutex
	dir         string
	limit, used int64
	items       map[string]*entry
	lock        *os.File
	closed      bool
	space       func() (free, total int64, err error)
	allocation  func(os.FileInfo) int64
}

// Stats reports physical allocation and the configured cache limit.
type Stats struct {
	Limit     int64 `json:"limit_bytes"`
	Allocated int64 `json:"allocated_bytes"`
	Chunks    int   `json:"chunks"`
}

// New opens one exclusively locked cache and recovers durable chunks.
func New(dir string, limit int64) (*Store, error) {
	if limit < 4096 {
		return nil, errors.New("cache limit must be at least 4096 bytes")
	}

	if err := os.MkdirAll(dir, 0o700); err != nil { // #nosec G703 -- The operator selects the private cache root at startup.
		return nil, err
	}

	lock, err := lockDirectory(dir)
	if err != nil {
		return nil, err
	}

	store := &Store{dir: dir, limit: limit, items: make(map[string]*entry), lock: lock, allocation: allocated}

	store.space = func() (int64, int64, error) { return diskSpace(dir) }
	if err = store.recoverDirectory(); err != nil {
		return nil, errors.Join(err, unlockDirectory(lock))
	}

	if err = store.reserve(0); err != nil {
		return nil, errors.Join(err, unlockDirectory(lock))
	}

	return store, nil
}

// Close refuses to release cache ownership while a reader or writer is active.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	for _, e := range s.items {
		if e.pins > 0 || e.done != nil {
			return errors.New("cache is in use")
		}
	}

	s.closed = true

	return unlockDirectory(s.lock)
}

// Stats reports physical allocation and the configured cache limit.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	return Stats{Limit: s.limit, Allocated: s.used, Chunks: len(s.items)}
}

// Resize applies a new limit only when enough unpinned data can be reclaimed.
func (s *Store) Resize(limit int64) error {
	if limit < 4096 {
		return errors.New("invalid cache limit")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	old := s.limit

	s.limit = limit
	if e := s.reserve(0); e != nil {
		s.limit = old
		return e
	}

	return nil
}
