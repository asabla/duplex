// Package cache implements a bounded, crash-recoverable source-chunk cache.
package cache

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := lockDirectory(dir)
	if err != nil {
		return nil, err
	}
	store := &Store{dir: dir, limit: limit, items: make(map[string]*entry), lock: lock}
	store.space = func() (int64, int64, error) { return diskSpace(dir) }
	if err = store.recoverDirectory(); err != nil {
		return nil, errors.Join(err, unlockDirectory(lock))
	}
	if err = store.reserve(0); err != nil {
		return nil, errors.Join(err, unlockDirectory(lock))
	}
	return store, nil
}

func (s *Store) recoverDirectory() error {
	files, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.Name() == ".lock" {
			continue
		}
		if err = s.recoverEntry(file); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) recoverEntry(file os.DirEntry) error {
	if file.IsDir() {
		return errors.New("unexpected directory in media cache")
	}
	path := filepath.Join(s.dir, file.Name())
	if strings.HasPrefix(file.Name(), ".part-") {
		return os.Remove(path)
	}
	key, digest, err := chunkName(file.Name())
	if err != nil {
		return err
	}
	info, err := file.Info()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("non-regular cache entry")
	}
	if s.items[key] != nil {
		return errors.New("duplicate cache identity")
	}
	allocation := allocated(info)
	if allocation > math.MaxInt64-s.used {
		return errors.New("cache allocation exceeds supported capacity")
	}
	s.items[key] = &entry{path: path, digest: digest, allocation: allocation, used: info.ModTime()}
	s.used += allocation
	return nil
}

func chunkName(name string) (key, digest string, err error) {
	parts := strings.Split(strings.TrimSuffix(name, ".chunk"), "-")
	if !strings.HasSuffix(name, ".chunk") || len(parts) != 2 || len(parts[0]) != 64 || len(parts[1]) != 64 {
		return "", "", errors.New("unexpected file in media cache")
	}
	if _, err = hex.DecodeString(parts[0] + parts[1]); err != nil {
		return "", "", err
	}
	return parts[0], parts[1], nil
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

// reserve is called with mu held, except during construction.
func (s *Store) reserve(n int64) error {
	for s.used > s.limit || n > s.limit-s.used {
		if err := s.evictOne(); err != nil {
			return err
		}
	}
	s.used += n
	return nil
}

// Called with mu held. Active readers and unfinished writes are never victims.
func (s *Store) evictOne() error {
	var key string
	var oldest *entry
	for k, e := range s.items {
		if e.pins == 0 && e.done == nil && (oldest == nil || e.used.Before(oldest.used)) {
			key, oldest = k, e
		}
	}
	if oldest == nil {
		return ErrFull
	}
	if err := os.Remove(oldest.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(s.items, key)
	s.used -= oldest.allocation
	return nil
}

// Reserve both the configured budget and physical capacity before fetch. Count
// all unfinished reservations conservatively, including partially written files.
// Leave up to 64 MiB (1% on small volumes) for filesystem and durable state writes.
func (s *Store) reserveFetch(n int64) error {
	for {
		free, total, err := s.space()
		if err != nil {
			return err
		}
		pending := int64(0)
		for _, e := range s.items {
			if e.done != nil {
				pending += e.allocation
			}
		}
		headroom := min(int64(64<<20), max(int64(4096), total/100))
		if s.used <= s.limit && n <= s.limit-s.used && pending <= math.MaxInt64-n-headroom && free >= n+pending+headroom {
			s.used += n
			return nil
		}
		if err = s.evictOne(); err != nil {
			return err
		}
	}
}
