package cache

import (
	"errors"
	"math"
	"os"
)

func chunkReservation(length int64) (int64, error) {
	payload := ((length + 4095) / 4096) * 4096
	metadata := int64(0)
	if payload > 4*4096 {
		// Fragmented files can allocate extent-tree blocks in addition to their
		// payload. Reserve one block per MiB, then charge the observed allocation.
		metadata = ((payload-1)/(1<<20) + 1) * 4096
	}
	if payload > math.MaxInt64-metadata {
		return 0, errors.New("invalid chunk allocation bounds")
	}
	return payload + metadata, nil
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
