package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

// fixtureHTTP is a qualification origin, not a production source adapter.
func fixtureHTTP(args []string) error {
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
		p := filepath.Join(*dir, name)
		source, e := os.Open(p)
		if e != nil {
			return e
		}
		h := sha256.New()
		if _, e = io.Copy(h, source); e != nil {
			source.Close()
			return e
		}
		source.Close()
		etag := `"` + hex.EncodeToString(h.Sum(nil)) + `"`
		mux.HandleFunc("GET /"+name, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if _, e := os.Stat(filepath.Join(*dir, "expired")); e == nil {
				http.Error(w, "fixture locator expired", 403)
				return
			}
			file, e := os.Open(p)
			if e != nil {
				http.Error(w, "fixture unavailable", 503)
				return
			}
			defer file.Close()
			stat, e := file.Stat()
			if e != nil {
				http.Error(w, "fixture unavailable", 503)
				return
			}
			w.Header().Set("ETag", etag)
			http.ServeContent(&countingWriter{ResponseWriter: w, count: &bytes}, r, name, stat.ModTime(), file)
		})
	}
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]int64{"requests": requests.Load(), "bytes": bytes.Load()})
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	e := server.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
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
