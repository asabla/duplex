package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/app"
	"duplex/internal/config"
	"duplex/internal/media"
	"duplex/internal/source"
)

func testWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testClose(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Error(err)
	}
}

func initializedConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := initialize([]string{"--config", file, "--state-dir", filepath.Join(dir, "state"), "--cache-dir", filepath.Join(dir, "chunks"), "--mountpoint", filepath.Join(dir, "library"), "--listen", "127.0.0.1:0"}); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestCommandHelpAndErrors(t *testing.T) {
	for _, name := range []string{"init", "serve", "backup", "health", "fixture-http", "check-plex", "gate"} {
		if err := execute(t.Context(), []string{name, "--help"}); !errors.Is(err, flag.ErrHelp) {
			t.Fatal(name, err)
		}
		if err := execute(t.Context(), []string{name, "--unknown-option"}); err == nil {
			t.Fatal("unknown flag accepted", name)
		}
	}
	for _, args := range [][]string{nil, {"unknown"}, {"backup"}, {"fixture-http"}, {"serve", "--config", "/does-not-exist"}, {"gate", "--fixture-episodes", "0"}, {"gate", "--combined-format", "18"}, {"gate"}, {"check-plex", "--url", "file:///invalid"}, {"check-plex", "--token-file", "/does-not-exist"}, {"health", "--url", ":bad"}} {
		if err := execute(t.Context(), args); err == nil {
			t.Fatal("invalid command succeeded", args)
		}
	}
}

func TestInitializationBackupAndHealth(t *testing.T) {
	file := initializedConfig(t)
	if err := initialize([]string{"--config", file}); err == nil {
		t.Fatal("overwrote initialized installation")
	}
	output := filepath.Join(t.TempDir(), "backup.sqlite")
	// The catalogue must first exist; backup must not invent an installation.
	if err := backup(t.Context(), []string{"--config", file, "--output", output}); err == nil {
		t.Fatal("backed up a missing catalogue")
	}
	manager, err := config.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.Open(t.Context(), manager, nilSource{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = application.Close(); err != nil {
		t.Fatal(err)
	}
	if err = backup(t.Context(), []string{"--config", file, "--output", output}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(output); err != nil || info.Size() == 0 {
		t.Fatal(info, err)
	}
	for _, code := range []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusTemporaryRedirect} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "http://127.0.0.1:1/")
			w.WriteHeader(code)
		}))
		err = health(t.Context(), []string{"--url", server.URL})
		server.Close()
		if (err == nil) != (code == http.StatusOK) {
			t.Fatal(code, err)
		}
	}
}

func TestLocalGateAndImmutableHTTPReads(t *testing.T) {
	dir := t.TempDir()
	video, audio := filepath.Join(dir, "video.mp4"), filepath.Join(dir, "audio.m4a")
	testWrite(t, video, []byte("controlled video bytes"))
	testWrite(t, audio, []byte("controlled audio bytes"))
	if _, err := openLocal(dir); err == nil {
		t.Fatal("directory accepted as fixture")
	}
	in := defaultGateInput()
	if err := in.load(t.Context(), gateOptions{video: video, audio: audio}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := in.close(); err != nil {
			t.Error(err)
		}
	}()
	buffer := make([]byte, 4)
	if n, err := in.video.ReadAt(t.Context(), buffer, 11); err != nil || n != 4 || string(buffer) != "vide" {
		t.Fatal(n, string(buffer), err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := in.video.ReadAt(ctx, buffer, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, _, err := gateRepresentation(t.Context(), in); err == nil {
		t.Fatal("invalid MP4 tracks accepted")
	}
	representation, spans, profile, err := gateRepresentation(t.Context(), gateInput{video: in.video})
	if err != nil || spans != 0 || representation != in.video {
		t.Fatal(representation, spans, err)
	}
	options := gateOptions{fixtureEpisodes: 3}
	entries, err := gateEntries(options, in, in.video)
	if err != nil || len(entries) != 7 {
		t.Fatal(len(entries), err)
	}
	fixture := filepath.Join(dir, "catalogue.json")
	testWrite(t, fixture, []byte(`[{"id":"episode-one","title":"Changed title","year":2025,"number":7}]`))
	options.fixtureCatalogue = fixture
	entries, err = gateEntries(options, in, in.video)
	if err != nil || len(entries) != 3 || !strings.Contains(entries[1].Path, "S2025E0007") {
		t.Fatal(entries, err)
	}
	testWrite(t, fixture, []byte("invalid"))
	if _, err = gateEntries(options, in, in.video); err == nil {
		t.Fatal("invalid catalogue accepted")
	}
	handler := gateHandler(options, in, representation, spans, profile)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://duplex.test/media.mp4", nil)
	request.Header.Set("Range", "bytes=11-14")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "vide" || response.Header().Get("ETag") == "" {
		t.Fatal(response)
	}
	for _, path := range []string{"/healthz", "/readyz", "/diagnostics"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatal(path, w)
		}
	}
}

func TestRemoteGateMetadataAndFixtureExpiry(t *testing.T) {
	dir := t.TempDir()
	testWrite(t, filepath.Join(dir, "video.mp4"), bytes.Repeat([]byte("v"), 4096))
	testWrite(t, filepath.Join(dir, "audio.m4a"), bytes.Repeat([]byte("a"), 1024))
	var requests, transferred atomic.Int64
	handler, err := fixtureHandler(dir, "video.mp4", &requests, &transferred)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	metadata := filepath.Join(dir, "source.json")
	data, err := json.Marshal(map[string]any{"id": "fixture", "channel_id": "owner", "channel": "Fixture owner", "title": "Fixture recording", "upload_date": "20251025", "live_status": "was_live", "formats": []map[string]string{{"format_id": "18", "url": server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	testWrite(t, metadata, data)
	options := gateOptions{sourceJSON: metadata, combinedFormat: "18", cacheDir: filepath.Join(dir, "cache"), cacheBytes: 1 << 20}
	in, err := loadRemoteGate(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if in.year != 2025 || in.channelTitle != "Fixture owner" || in.audio != nil {
		t.Fatal(in)
	}
	w := httptest.NewRecorder()
	gateHandler(options, in, in.video, 0, "combined").ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/diagnostics", nil))
	if !strings.Contains(w.Body.String(), "cache") {
		t.Fatal(w.Body.String())
	}
	if err = in.close(); err != nil {
		t.Fatal(err)
	}
	testWrite(t, filepath.Join(dir, "expired"), nil)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	expired := httptest.NewRecorder()
	handler(expired, req)
	if expired.Code != http.StatusForbidden || requests.Load() < 2 || transferred.Load() == 0 {
		t.Fatal(expired.Code, requests.Load(), transferred.Load())
	}
	failed, err := loadRemoteGate(t.Context(), options)
	if !errors.Is(err, media.ErrExpired) {
		t.Fatal("expired source accepted", err)
	}
	if closeErr := failed.close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	missing, err := loadRemoteGate(t.Context(), gateOptions{sourceJSON: "/does-not-exist"})
	if err == nil {
		t.Fatal("missing source metadata accepted")
	}
	if closeErr := missing.close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	for _, value := range []string{"invalid", `{"live_status":"is_live"}`} {
		testWrite(t, metadata, []byte(value))
		if _, err = loadRemoteGate(t.Context(), options); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
}

func TestHTTPServerStopsOnContextCancellation(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- serveHTTP(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeText(w, "ready") }))
	}()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
	cancel()
	select {
	case err = <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
	if err = notifyReady(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if err = notifyReady(t.Context(), filepath.Join(t.TempDir(), "missing.sock")); err == nil {
		t.Fatal("notification failure ignored")
	}
}

// This adapter is used only for an empty offline installation.
type nilSource struct{}

func (nilSource) ResolveChannel(context.Context, string) (source.Channel, error) {
	return source.Channel{}, errors.New("offline fixture")
}

func (nilSource) Enumerate(context.Context, string) (source.Channel, error) {
	return source.Channel{}, errors.New("offline fixture")
}

func (nilSource) Metadata(context.Context, string) (source.Metadata, error) {
	return source.Metadata{}, errors.New("offline fixture")
}

func (nilSource) Resolve(context.Context, string) (source.Metadata, error) {
	return source.Metadata{}, errors.New("offline fixture")
}

func unusedTestAddress(t *testing.T) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	testClose(t, listener)
	return address
}

func waitCommandHTTP(t *testing.T, address, path string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	for range 100 {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+address+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			return response
		}
		select {
		case <-t.Context().Done():
			t.Fatal("command did not start")
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("command listener did not become available")
	return nil
}

func stopTestCommand(t *testing.T, cancel context.CancelFunc, result <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command did not stop")
	}
}

func TestOfflineServiceStartsAndFlushesPrivateProfile(t *testing.T) {
	file := initializedConfig(t)
	manager, err := config.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Snapshot()
	snapshot.Config.Listen = unusedTestAddress(t)
	if err = manager.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}
	profiles := filepath.Join(t.TempDir(), "profiles")
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- serve(ctx, []string{"--config", file, "--no-mount", "--yt-dlp", filepath.Join(t.TempDir(), "missing"), "--profile-dir", profiles, "--profile-kind", "allocs", "--profile-duration", "1s"})
	}()
	response := waitCommandHTTP(t, snapshot.Config.Listen, "/healthz")
	if response.StatusCode != http.StatusOK {
		t.Error(response.StatusCode)
	}
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
	response = waitCommandHTTP(t, snapshot.Config.Listen, "/readyz")
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Error("unmounted service became ready")
	}
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
	stopTestCommand(t, cancel, result)
	entries, err := os.ReadDir(profiles)
	if err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
	}
	if _, err = os.Stat(filepath.Join(profiles, "allocs-measurements.json")); err != nil {
		t.Fatal(err)
	}
}

func TestFixtureOriginCommandAndCombinedGateStart(t *testing.T) {
	dir := t.TempDir()
	body := bytes.Repeat([]byte("a"), 4096)
	for _, name := range []string{"video.mp4", "audio.m4a"} {
		testWrite(t, filepath.Join(dir, name), body)
	}
	address := unusedTestAddress(t)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- fixtureHTTP(ctx, []string{"--dir", dir, "--listen", address}) }()
	response := waitCommandHTTP(t, address, "/stats")
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
	metadata := filepath.Join(dir, "source.json")
	data, err := json.Marshal(map[string]any{"id": "fixture", "live_status": "not_live", "formats": []map[string]string{{"format_id": "18", "url": "http://" + address + "/video.mp4"}}})
	if err != nil {
		t.Fatal(err)
	}
	testWrite(t, metadata, data)
	gateAddress := unusedTestAddress(t)
	gateContext, stopGate := context.WithCancel(t.Context())
	gateResult := make(chan error, 1)
	go func() {
		gateResult <- gateCommand(gateContext, []string{"--source-json", metadata, "--combined-format", "18", "--cache-dir", filepath.Join(dir, "cache"), "--cache-bytes", "1048576", "--listen", gateAddress})
	}()
	response = waitCommandHTTP(t, gateAddress, "/media.mp4")
	data, err = io.ReadAll(response.Body)
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
	if err != nil || !bytes.Equal(data, body) {
		t.Error(len(data), err)
	}
	stopTestCommand(t, stopGate, gateResult)
	stopTestCommand(t, cancel, result)
}

func TestPlexCheckUsesProtectedTokenFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "test-secret" {
			t.Error("token missing")
		}
		if r.URL.Path == "/identity" {
			writeText(w, `<MediaContainer version="1.43.5" claimed="1"/>`)
			return
		}
		writeText(w, `<MediaContainer><MetadataAgentProvider identifier="tv.plex.agents.nfo.series"><MetadataType type="2"/></MetadataAgentProvider></MediaContainer>`)
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "plex-token")
	testWrite(t, file, []byte("test-secret\n"))
	if err := checkPlex(t.Context(), []string{"--url", server.URL, "--token-file", file}); err != nil {
		t.Fatal(err)
	}
}

func TestHelpEntrypointDoesNotCreateInstallation(t *testing.T) {
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	file := filepath.Join(t.TempDir(), "config.yaml")
	os.Args = []string{"duplex", "init", "--help", "--config", file}
	main()
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("help created installation state", err)
	}
}
