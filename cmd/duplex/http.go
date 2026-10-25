package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

func writeText(w http.ResponseWriter, text string) {
	if _, err := io.WriteString(w, text); err != nil {
		slog.Debug("HTTP response ended before completion")
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Debug("JSON response ended before completion")
	}
}

const httpShutdownTimeout = 10 * time.Second

func serveHTTP(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	done := make(chan struct{})
	shutdownError := make(chan error, 1)
	go func() {
		select {
		case <-done:
			shutdownError <- nil
			return
		case <-ctx.Done():
		}
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), httpShutdownTimeout)
		defer cancel()
		err := server.Shutdown(shutdown)
		if err != nil {
			err = errors.Join(err, server.Close())
		}
		shutdownError <- err
	}()
	err := server.Serve(listener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return errors.Join(err, <-shutdownError)
}
