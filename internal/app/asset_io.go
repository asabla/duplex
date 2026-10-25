package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"duplex/internal/config"
	"duplex/internal/library"
	"duplex/internal/source"
)

// localAsset is bounded metadata, not an evictable media or transcode file.
type localAsset struct {
	file string
	size int64
}

func (r localAsset) ID() string { return filepath.Base(r.file) }

func (r localAsset) Size() int64 { return r.size }

func (r localAsset) ReadAt(ctx context.Context, p []byte, o int64) (n int, result error) {
	if e := ctx.Err(); e != nil {
		return 0, e
	}

	f, e := os.Open(r.file)
	if e != nil {
		return 0, e
	}

	defer func() { result = errors.Join(result, f.Close()) }()

	return f.ReadAt(p, o)
}

func (a *App) saveAsset(id, suffix string, b []byte) (library.Entry, error) {
	if !assetID.MatchString(id) {
		return library.Entry{}, errors.New("invalid asset identity")
	}

	dir := filepath.Join(a.original.DataDir, "assets")
	if e := os.MkdirAll(dir, 0o700); e != nil {
		return library.Entry{}, e
	}

	h := sha256.Sum256(b)

	file := filepath.Join(dir, id+"-"+hex.EncodeToString(h[:])+suffix)
	if e := config.AtomicWrite(file, b, 0o600); e != nil {
		return library.Entry{}, e
	}

	return library.Entry{Path: suffix, Media: localAsset{file: file, size: int64(len(b))}}, nil
}

func fetchAsset(ctx context.Context, raw string) (data []byte, result error) {
	if !source.AllowedURL(raw) {
		return nil, errors.New("unsupported asset host")
	}

	req, e := httpRequest(ctx, raw)
	if e != nil {
		return nil, e
	}

	r, e := source.HTTPClient().Do(req)
	if e != nil {
		return nil, errors.New("asset unavailable")
	}

	defer func() { result = errors.Join(result, r.Body.Close()) }()

	if r.StatusCode != 200 {
		return nil, errors.New("asset unavailable")
	}

	b, e := io.ReadAll(io.LimitReader(r.Body, 8<<20+1))
	if e != nil || len(b) > 8<<20 {
		return nil, errors.New("asset exceeds metadata size limit")
	}

	return b, nil
}

func httpRequest(ctx context.Context, raw string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
}
