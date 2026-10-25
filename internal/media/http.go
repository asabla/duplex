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

// HTTPSource binds reads to one exact-size upstream object and cache namespace.
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
func OpenHTTP(ctx context.Context, client *http.Client, raw, identity string, store cache.ChunkStore) (*HTTPSource, error) {
	return openHTTP(ctx, client, raw, identity, store, SourceVersion{})
}

// SourceVersion is an opaque version from the authenticated source adapter.
// It is never derived from a lease URL alone. Size must match the range probe.
type SourceVersion struct {
	Key  string
	Size int64
}

// OpenVersionedHTTP also validates fresh start, middle, and end fingerprints
// before allowing a provider version to reuse cached chunks or packaging indexes.
func OpenVersionedHTTP(ctx context.Context, client *http.Client, raw, identity string, store cache.ChunkStore, version SourceVersion) (*HTTPSource, error) {
	return openHTTP(ctx, client, raw, identity, store, version)
}

func openHTTP(ctx context.Context, client *http.Client, raw, identity string, store cache.ChunkStore, version SourceVersion) (*HTTPSource, error) {
	if client == nil || identity == "" {
		return nil, errors.New("HTTP client and source identity are required")
	}
	s := &HTTPSource{client: client, url: raw, cache: store}
	if err := s.probe(ctx); err != nil {
		return nil, err
	}
	scope, err := s.namespace(ctx, version)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256([]byte(identity + ":" + strconv.FormatInt(s.size, 10) + ":" + scope))
	s.id = hex.EncodeToString(h[:])
	return s, nil
}

func (s *HTTPSource) probe(ctx context.Context) error {
	resp, err := s.request(ctx, 0, 1)
	if err != nil {
		return err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2))
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil || len(body) != 1 {
		return errors.New("invalid range probe body")
	}
	var start, end, total int64
	value := strings.TrimSpace(resp.Header.Get("Content-Range"))
	if _, err = fmt.Sscanf(value, "bytes %d-%d/%d", &start, &end, &total); err != nil || start != 0 || end != 0 || total <= 0 || value != fmt.Sprintf("bytes 0-0/%d", total) {
		return errors.New("source has no exact byte-range size")
	}
	s.size = total
	s.etag = resp.Header.Get("ETag")
	if strings.HasPrefix(s.etag, "W/") {
		s.etag = ""
	}
	s.modified = resp.Header.Get("Last-Modified")
	s.UpstreamBytes.Add(1)
	return nil
}

func (s *HTTPSource) namespace(ctx context.Context, version SourceVersion) (string, error) {
	if version.Key != "" && version.Size != s.size {
		return "", ErrChanged
	}
	if s.etag != "" {
		return s.etag, nil
	}
	if version.Key == "" {
		return s.url + ":" + strconv.FormatInt(time.Now().UnixNano(), 10), nil
	}
	decoded, err := hex.DecodeString(version.Key)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("invalid provider version")
	}
	h := sha256.New()
	h.Write(decoded)
	h.Write([]byte(s.modified))
	for _, offset := range []int64{0, s.size / 2, max(0, s.size-65536)} {
		data, err := s.fetch(ctx, offset, int(min(int64(65536), s.size-offset)))
		if err != nil {
			return "", err
		}
		h.Write([]byte(strconv.FormatInt(offset, 10)))
		h.Write(data)
	}
	return "provider-v1:" + hex.EncodeToString(h.Sum(nil)), nil
}

// ID returns the immutable namespace verified when the source was opened.
func (s *HTTPSource) ID() string { return s.id }

// Size returns the exact length established by an upstream range response.
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

// ReadAt reads only requested ranges and pins cache chunks until the copy completes.
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
