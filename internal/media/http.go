package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"duplex/internal/cache"
)

const ChunkSize = 1024 * 1024

type HTTPSource struct {
	client             *http.Client
	url                string
	id, etag, modified string
	size               int64
	cache              cache.ChunkStore
	UpstreamBytes      atomic.Int64
	Requests           atomic.Int64
}

// OpenHTTP probes a byte, never the whole object. Callers must supply a client
// with appropriate destination and redirect restrictions for untrusted URLs.
func OpenHTTP(ctx context.Context, client *http.Client, url, identity string, store cache.ChunkStore) (*HTTPSource, error) {
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	s := &HTTPSource{client: client, url: url, cache: store}
	resp, err := s.request(ctx, 0, 1)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var start, end, total int64
	if _, err = fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil || start != 0 || end != 0 || total <= 0 || strings.TrimSpace(resp.Header.Get("Content-Range")) != fmt.Sprintf("bytes %d-%d/%d", start, end, total) {
		return nil, errors.New("source has no exact byte-range size")
	}
	s.size = total
	s.etag = resp.Header.Get("ETag")
	if strings.HasPrefix(s.etag, "W/") {
		s.etag = ""
	}
	s.modified = resp.Header.Get("Last-Modified")
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2))
	if err != nil || len(b) != 1 {
		return nil, errors.New("invalid range probe body")
	}
	s.UpstreamBytes.Add(1)
	// Without a strong validator, do not reuse data across resolutions. A signed
	// URL can expire, but must never become a durable identity on its own.
	scope := s.etag
	if scope == "" {
		scope = url + ":" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	h := sha256.Sum256([]byte(identity + ":" + strconv.FormatInt(total, 10) + ":" + scope))
	s.id = hex.EncodeToString(h[:])
	return s, nil
}

func (s *HTTPSource) ID() string  { return s.id }
func (s *HTTPSource) Size() int64 { return s.size }

func (s *HTTPSource) request(ctx context.Context, offset int64, length int) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, errors.New("invalid source locator")
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+int64(length)-1))
	req.Header.Set("Accept-Encoding", "identity")
	if s.etag != "" {
		req.Header.Set("If-Match", s.etag)
	}
	resp, err := s.client.Do(req)
	s.Requests.Add(1)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return nil, context.DeadlineExceeded
		}
		return nil, errors.New("source request failed")
	}
	if resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		switch resp.StatusCode {
		case 401, 403:
			return nil, fmt.Errorf("%w (HTTP %d, offset %d)", ErrExpired, resp.StatusCode, offset)
		case 412:
			return nil, ErrChanged
		}
		return nil, fmt.Errorf("source rejected byte range (HTTP %d)", resp.StatusCode)
	}
	if resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity" {
		resp.Body.Close()
		return nil, errors.New("encoded range response")
	}
	return resp, nil
}

func (s *HTTPSource) fetch(ctx context.Context, offset int64, length int) ([]byte, error) {
	resp, err := s.request(ctx, offset, length)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var start, end, total int64
	if _, err = fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil || start != offset || end != offset+int64(length)-1 || total != s.size || strings.TrimSpace(resp.Header.Get("Content-Range")) != fmt.Sprintf("bytes %d-%d/%d", start, end, total) {
		return nil, ErrChanged
	}
	if s.etag != "" && resp.Header.Get("ETag") != s.etag {
		return nil, ErrChanged
	}
	if s.modified != "" && resp.Header.Get("Last-Modified") != s.modified {
		return nil, ErrChanged
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(length)+1))
	s.UpstreamBytes.Add(int64(len(b)))
	if err != nil {
		return nil, errors.New("source body read failed")
	}
	if len(b) != length {
		return nil, errors.New("source range has incorrect length")
	}
	return b, nil
}

func (s *HTTPSource) ReadAt(ctx context.Context, p []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("negative offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if offset >= s.size {
		return 0, io.EOF
	}
	wanted := len(p)
	p = p[:min(int64(len(p)), s.size-offset)]
	n := 0
	for n < len(p) {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		start := (offset / ChunkSize) * ChunkSize
		length := int(min(int64(ChunkSize), s.size-start))
		var b []byte
		var release func()
		var err error
		if s.cache != nil {
			b, release, err = s.cache.Get(ctx, s.id, start, length, func(c context.Context) ([]byte, error) { return s.fetch(c, start, length) })
		} else {
			b, err = s.fetch(ctx, start, length)
			release = func() {}
		}
		if err != nil {
			return n, err
		}
		count := copy(p[n:], b[offset-start:])
		release()
		n += count
		offset += int64(count)
	}
	if n < wanted {
		return n, io.EOF
	}
	return n, nil
}
