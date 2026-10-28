package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMountLossStopsHTTPService(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	close(done)

	err = serveMountedHTTP(t.Context(), listener, http.NotFoundHandler(), done)
	if err == nil || !strings.Contains(err.Error(), "mount helper connection lost") {
		t.Fatal(err)
	}

	listener, err = (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()

	if err = serveMountedHTTP(ctx, listener, http.NotFoundHandler(), nil); err != nil {
		t.Fatal(err)
	}

	if err = execute(t.Context(), []string{"mount-helper", "/arbitrary/path"}); err == nil {
		t.Fatal("helper accepted configurable mount arguments")
	}
}
