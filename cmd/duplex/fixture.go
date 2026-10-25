package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
)

// fixtureHTTP is a qualification origin, not a production source adapter.
func fixtureHTTP(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("fixture-http", flag.ContinueOnError)
	dir := f.String("dir", "", "directory containing controlled video.mp4 and audio.m4a")

	listen := f.String("listen", "127.0.0.1:8080", "fixture origin address")
	if e := f.Parse(args); e != nil {
		return e
	}

	if *dir == "" {
		return errors.New("fixture directory required")
	}

	mux := http.NewServeMux()

	var requests, bytes atomic.Int64
	for _, name := range []string{"video.mp4", "audio.m4a"} {
		handler, err := fixtureHandler(*dir, name, &requests, &bytes)
		if err != nil {
			return err
		}

		mux.HandleFunc("GET /"+name, handler)
	}

	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]int64{"requests": requests.Load(), "bytes": bytes.Load()})
	})

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
	if err != nil {
		return err
	}

	return serveHTTP(ctx, listener, mux)
}

func fixtureHandler(dir, name string, requests, bytes *atomic.Int64) (http.HandlerFunc, error) {
	path := filepath.Join(dir, name)

	source, err := os.Open(path) //nolint:gosec // G304: The operator selects the controlled fixture directory.
	if err != nil {
		return nil, err
	}

	h := sha256.New()

	_, err = io.Copy(h, source)
	if err = errors.Join(err, source.Close()); err != nil {
		return nil, err
	}

	etag := `"` + hex.EncodeToString(h.Sum(nil)) + `"`

	return func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)

		if _, err := os.Stat(filepath.Join(dir, "expired")); err == nil {
			http.Error(w, "fixture locator expired", http.StatusForbidden)
			return
		}

		file, err := os.Open(path) //nolint:gosec // G304: Only the two fixed fixture names are served from the operator-selected directory.
		if err != nil {
			http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
			return
		}

		defer func() {
			if err := file.Close(); err != nil {
				slog.Error("fixture close failed")
			}
		}()

		info, err := file.Stat()
		if err != nil {
			http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("ETag", etag)
		http.ServeContent(&countingWriter{ResponseWriter: w, count: bytes}, r, name, info.ModTime(), file)
	}, nil
}

type countingWriter struct {
	http.ResponseWriter
	count *atomic.Int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, e := w.ResponseWriter.Write(p)
	w.count.Add(int64(n))

	return n, e
}

type local struct {
	f    *os.File
	size int64
	id   string
}

func (l *local) ID() string { return l.id }

func (l *local) Size() int64 { return l.size }

func (l *local) ReadAt(ctx context.Context, p []byte, o int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	return l.f.ReadAt(p, o)
}
