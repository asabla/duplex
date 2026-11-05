package media

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"duplex/internal/cache"
)

const (
	retryEntries = 128
	minimumRetry = time.Second
	maximumRetry = 5 * time.Minute
)

type retryDeadline struct {
	until time.Time
	used  time.Time
}

// RetryPolicy shares bounded rate-limit delays across readers and renewed
// locators. Keys are fixed-size source-identity hashes, never signed URLs.
// The zero value is ready for use; do not copy it after first use.
type RetryPolicy struct {
	mu      sync.Mutex
	entries map[string]retryDeadline
}

// OpenVersionedHTTP shares retry admission for probes and payload requests.
// Verified cached bytes do not enter this admission path.
func (p *RetryPolicy) OpenVersionedHTTP(ctx context.Context, client *http.Client, raw, identity string, store cache.ChunkStore, version SourceVersion) (*HTTPSource, error) {
	return openHTTP(ctx, client, raw, identity, store, version, p)
}

func (p *RetryPolicy) blocked(identity string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.entries[identity]
	if !ok || !now.Before(entry.until) {
		delete(p.entries, identity)
		return false
	}

	entry.used = now
	p.entries[identity] = entry

	return true
}

func (p *RetryPolicy) postpone(identity, header string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.entries == nil {
		p.entries = make(map[string]retryDeadline)
	}

	for key, entry := range p.entries {
		if !now.Before(entry.until) {
			delete(p.entries, key)
		}
	}

	until := now.Add(retryDelay(header, now))
	if previous := p.entries[identity]; previous.until.After(until) {
		until = previous.until
	}

	p.entries[identity] = retryDeadline{until: until, used: now}

	if len(p.entries) > retryEntries {
		oldest := ""
		for key, entry := range p.entries {
			if oldest == "" || entry.used.Before(p.entries[oldest].used) {
				oldest = key
			}
		}

		delete(p.entries, oldest)
	}
}

func retryDelay(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)

	seconds, err := strconv.ParseUint(header, 10, 64)
	if err == nil || errors.Is(err, strconv.ErrRange) {
		if seconds >= uint64(maximumRetry/time.Second) {
			return maximumRetry
		}

		return max(minimumRetry, time.Duration(seconds)*time.Second)
	}

	date, err := http.ParseTime(header)
	if err != nil {
		return minimumRetry
	}

	return min(maximumRetry, max(minimumRetry, date.Sub(now)))
}
