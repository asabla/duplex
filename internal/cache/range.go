package cache

import (
	"context"
	"errors"
)

// ReadCached copies a range from a pinned, verified immutable chunk. The bounded
// memory snapshot is private: Get and Lookup still return independent buffers.
// A damaged disk copy cannot change bytes already verified in this snapshot.
func (s *Store) ReadCached(ctx context.Context, identity string, offset int64, length int, p []byte, within int) (int, error) {
	if within < 0 || within >= length {
		return 0, errors.New("invalid cached range")
	}

	key, item, err := s.pinPublished(ctx, identity, offset, length)
	if err != nil {
		return 0, err
	}

	data, release, err := s.snapshotChunk(key, item, length)
	if err != nil {
		return 0, err
	}
	defer release()

	return copy(p, data[within:]), nil
}

func (s *Store) snapshotChunk(key string, item *entry, length int) ([]byte, func(), error) {
	item.readMu.Lock()
	defer item.readMu.Unlock()

	s.mu.Lock()
	data := s.hot[key]
	s.mu.Unlock()

	if data != nil {
		return data, s.releaseChunk(item), nil
	}

	data, release, err := s.readChunk(key, item, length)
	if err != nil {
		return nil, nil, err
	}

	if length <= 1<<20 {
		s.mu.Lock()
		if s.hot == nil {
			s.hot = map[string][]byte{}
		}

		if s.items[key] == item && item.err == nil {
			s.dropHotLocked(key)
			s.hot[key] = data
			s.hotBytes += int64(cap(data))
		}

		s.trimHotLocked()
		s.mu.Unlock()
	}

	return data, release, nil
}

func (s *Store) dropHotLocked(key string) {
	s.hotBytes -= int64(cap(s.hot[key]))
	delete(s.hot, key)
}

func (s *Store) trimHotLocked() {
	for len(s.hot) > 128 || s.hotBytes > min(int64(32<<20), s.limit) {
		oldest := ""
		for key := range s.hot {
			if oldest == "" || s.items[key].used.Before(s.items[oldest].used) {
				oldest = key
			}
		}

		s.dropHotLocked(oldest)
	}
}
