package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ErrMiss means no verified, published local chunk is available.
var ErrMiss = errors.New("media chunk is not cached")

func chunkKey(identity string, offset int64, length int) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", identity, offset, length)))
	return hex.EncodeToString(hash[:])
}

// Lookup pins and verifies existing bytes. It never reserves, fetches, or
// publishes data. In-progress fetches remain owned by their original callers.
func (s *Store) Lookup(ctx context.Context, identity string, offset int64, length int) ([]byte, func(), error) {
	key, item, err := s.pinPublished(ctx, identity, offset, length)
	if err != nil {
		return nil, nil, err
	}

	return s.readChunk(key, item, length)
}

func (s *Store) pinPublished(ctx context.Context, identity string, offset int64, length int) (string, *entry, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}

	if identity == "" || offset < 0 || length <= 0 {
		return "", nil, ErrMiss
	}

	key := chunkKey(identity, offset, length)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", nil, errors.New("cache is closed")
	}

	item := s.items[key]
	if item == nil || item.done != nil || item.err != nil {
		s.mu.Unlock()
		return "", nil, ErrMiss
	}

	item.pins++
	item.used = time.Now()
	s.mu.Unlock()

	return key, item, nil
}
