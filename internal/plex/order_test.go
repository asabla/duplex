package plex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEpisodeOrderUsesOnlyValidatedItemAndIndex(t *testing.T) {
	var requests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)

		query := r.URL.Query()
		if r.Method != http.MethodPut || r.URL.Path != "/library/sections/7/all" || query.Get("type") != "4" || query.Get("id") != "42" || query.Get("index.value") != "3" || query.Has("index.locked") || r.Header.Get("X-Plex-Token") != "test-secret" || query.Has("X-Plex-Token") {
			t.Error("unexpected episode order request")
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := New(server.URL, "test-secret")
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []struct {
		section, key string
		index        int
	}{{"../other", "42", 3}, {"7", "42?secret", 3}, {"7", "0", 3}, {"7", "42", 0}, {"7", "42", -1}} {
		if err = client.SetEpisodeOrder(t.Context(), value.section, value.key, value.index); err == nil {
			t.Fatal("unsafe order request accepted", value)
		}
	}

	if requests.Load() != 0 {
		t.Fatal("invalid order reached Plex")
	}

	if err = client.SetEpisodeOrder(t.Context(), "7", "42", 3); err != nil || requests.Load() != 1 {
		t.Fatal(err, requests.Load())
	}
}

func TestScannerStateRequiresExplicitKnownSectionResult(t *testing.T) {
	for _, value := range []struct {
		response string
		busy     bool
		fails    bool
	}{
		{`<Directory key="7" refreshing="1"/>`, true, false},
		{`<Directory key="7" refreshing="0"/>`, false, false},
		{`<Directory key="7"/>`, false, true},
		{`<Directory key="8" refreshing="0"/>`, false, true},
		{`<invalid`, false, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeTestResponse(t, w, []byte("<MediaContainer>"+value.response+"</MediaContainer>"))
		}))

		client, err := New(server.URL, "")
		if err != nil {
			t.Fatal(err)
		}

		busy, err := client.libraryScanning(t.Context(), "7")

		server.Close()

		if busy != value.busy || (err != nil) != value.fails {
			t.Fatal(value, busy, err)
		}
	}
}

func TestWaitScanCompletesAndHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestResponse(t, w, []byte(`<MediaContainer><Directory key="7" refreshing="0"/></MediaContainer>`))
	}))
	defer server.Close()

	client, err := New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}

	if err = client.WaitScan(t.Context(), "7"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err = client.WaitScan(ctx, "7"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled scan wait did not stop", err)
	}

	if err = client.WaitScan(t.Context(), "../invalid"); err == nil {
		t.Fatal("invalid section accepted")
	}
}
