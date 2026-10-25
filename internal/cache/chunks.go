package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Get reserves unfinished writes, deduplicates fetches, and pins published data.
// A failed cleanup remains accounted for until eviction or a later retry removes it.
func (s *Store) Get(ctx context.Context, identity string, offset int64, length int, fetch func(context.Context) ([]byte, error)) ([]byte, func(), error) {
	if length <= 0 || offset < 0 || int64(length) > math.MaxInt64-4095 || offset > math.MaxInt64-int64(length) {
		return nil, nil, errors.New("invalid chunk bounds")
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", identity, offset, length)))
	key := hex.EncodeToString(hash[:])
	reservation := ((int64(length) + 4095) / 4096) * 4096
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		item, created, done, err := s.acquireChunk(key, reservation)
		if err != nil {
			return nil, nil, err
		}
		if done != nil {
			if err = s.waitChunk(ctx, item, done); err != nil {
				return nil, nil, err
			}
			continue
		}
		if created {
			return s.fetchChunk(ctx, key, item, length, fetch)
		}
		return s.readChunk(key, item, length)
	}
}

func (s *Store) acquireChunk(key string, reservation int64) (*entry, bool, <-chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false, nil, errors.New("cache is closed")
	}
	if item := s.items[key]; item != nil {
		if item.done != nil {
			return item, false, item.done, nil
		}
		if item.err == nil {
			item.pins++
			item.used = time.Now()
			return item, false, nil, nil
		}
		if item.pins > 0 {
			return nil, false, nil, item.err
		}
		if err := removeChunkFile(item.path); err != nil {
			return nil, false, nil, err
		}
		delete(s.items, key)
		s.used -= item.allocation
	}
	if reservation > s.limit {
		return nil, false, nil, ErrFull
	}
	if err := s.reserveFetch(reservation); err != nil {
		return nil, false, nil, err
	}
	item := &entry{done: make(chan struct{}), allocation: reservation, pins: 1, used: time.Now()}
	s.items[key] = item
	return item, true, nil, nil
}

func (s *Store) waitChunk(ctx context.Context, item *entry, done <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return item.err
}

func (s *Store) releaseChunk(item *entry) func() {
	var once sync.Once
	return func() { once.Do(func() { s.mu.Lock(); item.pins--; s.mu.Unlock() }) }
}

func (s *Store) readChunk(key string, item *entry, length int) ([]byte, func(), error) {
	data, err := readChunkFile(item.path, length)
	digest := sha256.Sum256(data)
	if err == nil && len(data) == length && hex.EncodeToString(digest[:]) == item.digest {
		return data, s.releaseChunk(item), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item.pins--
	item.err = errors.New("corrupt cache entry; retry after invalidation")
	if item.pins == 0 {
		if err = removeChunkFile(item.path); err != nil {
			return nil, nil, err
		}
		delete(s.items, key)
		s.used -= item.allocation
	}
	return nil, nil, item.err
}

func (s *Store) fetchChunk(ctx context.Context, key string, item *entry, length int, fetch func(context.Context) ([]byte, error)) ([]byte, func(), error) {
	data, err := fetch(ctx)
	if err == nil && len(data) != length {
		err = errors.New("short upstream chunk")
	}
	temp := ""
	if err == nil {
		temp, err = s.writeChunk(data, item.allocation)
	}
	if err == nil {
		digest := sha256.Sum256(data)
		item.digest = hex.EncodeToString(digest[:])
		item.path = filepath.Join(s.dir, key+"-"+item.digest+".chunk")
		err = os.Rename(temp, item.path) // #nosec G703 -- Both paths are constructed in the private cache from a generated temporary name and a hashed chunk key.
		if err == nil {
			err = syncChunkDirectory(s.dir)
		}
	}
	retained := ""
	if err != nil {
		var cleanupErr error
		retained, cleanupErr = cleanupChunk(temp, item.path)
		err = errors.Join(err, cleanupErr)
	}
	s.finishChunk(key, item, err, retained)
	if err != nil {
		return nil, nil, err
	}
	return data, s.releaseChunk(item), nil
}

func (s *Store) finishChunk(key string, item *entry, failure error, retained string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	done := item.done
	item.err = failure
	item.done = nil
	if failure != nil {
		item.pins = 0
		if retained == "" {
			delete(s.items, key)
			s.used -= item.allocation
		} else {
			item.path = retained
		}
	}
	close(done)
}

func (s *Store) writeChunk(data []byte, reservation int64) (name string, err error) {
	file, err := os.CreateTemp(s.dir, ".part-")
	if err != nil {
		return "", err
	}
	name = file.Name()
	written, err := file.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return name, err
	}
	info, err := os.Stat(name) // #nosec G703 -- The writer selects this private temporary chunk path.
	if err != nil {
		return name, err
	}
	if allocated(info) > reservation {
		return name, errors.New("cache allocation exceeds reservation")
	}
	return name, nil
}

func syncChunkDirectory(path string) error {
	directory, err := os.Open(path) //nolint:gosec // G304: The cache store selects this private chunk directory.
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func removeChunkFile(path string) error {
	if path == "" {
		return nil
	}
	err := os.Remove(path) // #nosec G703 -- Cleanup paths are constructed by the cache store; no client path is accepted.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func cleanupChunk(temporary, published string) (retained string, err error) {
	if failure := removeChunkFile(temporary); failure != nil {
		retained = temporary
		err = failure
	}
	if failure := removeChunkFile(published); failure != nil {
		retained = published
		err = errors.Join(err, failure)
	}
	return retained, err
}

func readChunkFile(path string, length int) (data []byte, err error) {
	file, err := os.Open(path) //nolint:gosec // G304: The cache store constructs chunk paths from hashed immutable identities.
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return io.ReadAll(io.LimitReader(file, int64(length)+1))
}
