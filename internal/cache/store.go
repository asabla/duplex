// Package cache implements a bounded, crash-recoverable source-chunk cache.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	success := false
	defer func() {
		if !success {
			unlockDirectory(lock)
		}
	}()
	s := &Store{dir: dir, limit: limit, items: make(map[string]*entry)}
	s.space = func() (int64, int64, error) { return diskSpace(dir) }
	s.lock = lock
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.Name() == ".lock" {
			continue
		}
		if f.IsDir() {
			return nil, errors.New("unexpected directory in media cache")
		}
		path := filepath.Join(dir, f.Name())
		if strings.HasPrefix(f.Name(), ".part-") {
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			continue
		}
		parts := strings.Split(strings.TrimSuffix(f.Name(), ".chunk"), "-")
		if !strings.HasSuffix(f.Name(), ".chunk") || len(parts) != 2 || len(parts[0]) != 64 || len(parts[1]) != 64 {
			return nil, errors.New("unexpected file in media cache")
		}
		if _, err := hex.DecodeString(parts[0] + parts[1]); err != nil {
			return nil, err
		}
		st, err := f.Info()
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			return nil, errors.New("non-regular cache entry")
		}
		allocation := allocated(st)
		if _, exists := s.items[parts[0]]; exists {
			return nil, errors.New("duplicate cache identity")
		}
		s.items[parts[0]] = &entry{path: path, digest: parts[1], allocation: allocation, used: st.ModTime()}
		s.used += allocation
	}
	if err := s.reserve(0); err != nil {
		return nil, err
	}
	success = true
	return s, nil
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
	for s.used+n > s.limit {
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
		if s.used+n <= s.limit && free >= n+pending+headroom {
			s.used += n
			return nil
		}
		if err = s.evictOne(); err != nil {
			return err
		}
	}
}

// Get pins the entry until release. Concurrent callers share one fetch. The
// fetch callback must honor ctx and must return exactly length bytes.
func (s *Store) Get(ctx context.Context, identity string, offset int64, length int, fetch func(context.Context) ([]byte, error)) ([]byte, func(), error) {
	if length <= 0 || offset < 0 {
		return nil, nil, errors.New("invalid chunk bounds")
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", identity, offset, length)))
	key := hex.EncodeToString(h[:])
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, nil, errors.New("cache is closed")
		}
		if e, ok := s.items[key]; ok {
			if e.done != nil {
				done := e.done
				s.mu.Unlock()
				select {
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				case <-done:
				}
				if e.err != nil {
					return nil, nil, e.err
				}
				continue
			}
			e.pins++
			e.used = time.Now()
			s.mu.Unlock()
			data, err := os.ReadFile(e.path)
			digest := sha256.Sum256(data)
			if err != nil || len(data) != length || hex.EncodeToString(digest[:]) != e.digest {
				s.mu.Lock()
				e.pins--
				if e.pins == 0 {
					if er := os.Remove(e.path); er != nil && !os.IsNotExist(er) {
						s.mu.Unlock()
						return nil, nil, er
					}
					delete(s.items, key)
					s.used -= e.allocation
				}
				s.mu.Unlock()
				return nil, nil, errors.New("corrupt cache entry; retry after invalidation")
			}
			var once sync.Once
			return data, func() { once.Do(func() { s.mu.Lock(); e.pins--; s.mu.Unlock() }) }, nil
		}
		reservation := int64((length+4095)/4096) * 4096
		if err := s.reserveFetch(reservation); err != nil {
			s.mu.Unlock()
			return nil, nil, err
		}
		e := &entry{done: make(chan struct{}), allocation: reservation, pins: 1, used: time.Now()}
		s.items[key] = e
		s.mu.Unlock()
		data, err := fetch(ctx)
		if err == nil && len(data) != length {
			err = errors.New("short upstream chunk")
		}
		var temp string
		if err == nil {
			f, er := os.CreateTemp(s.dir, ".part-")
			err = er
			if err == nil {
				temp = f.Name()
				_, err = f.Write(data)
				if err == nil {
					err = f.Sync()
				}
				if er = f.Close(); err == nil {
					err = er
				}
				if err == nil {
					var st os.FileInfo
					st, err = os.Stat(temp)
					if err == nil && allocated(st) > reservation {
						err = errors.New("cache allocation exceeds reservation")
					}
				}
			}
		}
		if err == nil {
			digest := sha256.Sum256(data)
			e.digest = hex.EncodeToString(digest[:])
			e.path = filepath.Join(s.dir, key+"-"+e.digest+".chunk")
			err = os.Rename(temp, e.path)
			if err == nil {
				d, er := os.Open(s.dir)
				if er == nil {
					er = d.Sync()
					_ = d.Close()
				}
				err = er
			}
		}
		if err != nil {
			if temp != "" {
				_ = os.Remove(temp)
			}
			if e.path != "" {
				_ = os.Remove(e.path)
			}
		}
		s.mu.Lock()
		done := e.done
		e.err = err
		e.done = nil
		if err != nil {
			delete(s.items, key)
			s.used -= reservation
		}
		close(done)
		s.mu.Unlock()
		if err != nil {
			return nil, nil, err
		}
		var once sync.Once
		return data, func() { once.Do(func() { s.mu.Lock(); e.pins--; s.mu.Unlock() }) }, nil
	}
}
