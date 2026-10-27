package plex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIdleMetadataWaitDoesNotDelayAnIdleScanner(t *testing.T) {
	var busy atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		value := "0"
		if busy.Load() {
			value = "1"
		}

		if _, err := w.Write([]byte(`<MediaContainer><Directory key="7" refreshing="` + value + `"/></MediaContainer>`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	if err = client.WaitIdle(ctx, "7"); err != nil {
		t.Fatal("idle scanner waited for the polling timer", err)
	}

	busy.Store(true)

	if err = client.WaitIdle(ctx, "7"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("busy scanner was not awaited", err)
	}

	if err = client.WaitIdle(ctx, "../7"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatal("invalid library accepted", err)
	}

	if err = client.WaitIdle(ctx, "7"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cancelled context ignored", err)
	}
}
