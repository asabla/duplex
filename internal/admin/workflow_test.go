package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"duplex/internal/app"
	"duplex/internal/config"
	"duplex/internal/source"
)

const workflowChannel = "UCaaaaaaaaaaaaaaaaaaaaaa"

type workflowSource struct{}

func (workflowSource) ResolveChannel(ctx context.Context, _ string) (source.Channel, error) {
	return source.Channel{ID: workflowChannel, Title: "Controlled channel"}, ctx.Err()
}

func (workflowSource) Enumerate(ctx context.Context, _ string) (source.Channel, error) {
	return source.Channel{ID: workflowChannel, Title: "Controlled channel", Entries: []source.Entry{{ID: "aqz-KE-bpKQ"}}}, ctx.Err()
}

func (workflowSource) Metadata(ctx context.Context, id string) (source.Metadata, error) {
	return source.Metadata{ID: id, ChannelID: workflowChannel, Channel: "Controlled channel", Title: "Controlled recording", UploadDate: "20261025", Duration: 60, LiveStatus: "not_live"}, ctx.Err()
}

func (s workflowSource) Resolve(ctx context.Context, id string) (source.Metadata, error) {
	return s.Metadata(ctx, id)
}

func (workflowSource) Entries(ctx context.Context, scope source.Scope, consume func([]source.Entry) error) (source.Listing, error) {
	if scope.Tab != "videos" && scope.PlaylistID == "" {
		return source.Listing{Complete: true}, ctx.Err()
	}
	err := consume([]source.Entry{{ID: "aqz-KE-bpKQ", Title: "Controlled recording", Position: 1}})
	return source.Listing{Count: 1, Complete: true}, err
}

func (workflowSource) Playlists(ctx context.Context, _ string) ([]source.Playlist, error) {
	return []source.Playlist{{ID: "PLfixture0001", ChannelID: workflowChannel, Title: "Controlled playlist"}}, ctx.Err()
}

type workflowFixture struct {
	app      *app.App
	server   *httptest.Server
	client   *http.Client
	password string
	csrf     string
}

func newWorkflowFixture(t *testing.T) *workflowFixture {
	t.Helper()
	root := t.TempDir()
	file := filepath.Join(root, "config.yaml")
	if err := config.Create(file, filepath.Join(root, "state")); err != nil {
		t.Fatal(err)
	}
	manager, err := config.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	settings := manager.Snapshot().Config
	passwordFile, err := InitCredentials(filepath.Join(settings.DataDir, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	password, err := os.ReadFile(passwordFile) //nolint:gosec // G304: This reads only credentials generated inside this test's temporary state.
	if err != nil {
		t.Fatal(err)
	}
	auth, err := OpenAuth(filepath.Join(settings.DataDir, "secrets", "admin.json"))
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.Open(t.Context(), manager, workflowSource{}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTest(t, application) })
	admin, err := New(application, auth)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(admin.Handler())
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return &workflowFixture{app: application, server: server, client: client, password: strings.TrimSpace(string(password))}
}

func (f *workflowFixture) request(t *testing.T, method, path, contentType string, body io.Reader, status int) ([]byte, http.Header) {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), method, f.server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Origin", f.server.URL)
	if f.csrf != "" {
		r.Header.Set("X-CSRF-Token", f.csrf)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	response, err := f.client.Do(r)
	if err != nil {
		t.Fatal(method, path, err)
	}
	defer closeTest(t, response.Body)
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s: HTTP %d, want %d", method, path, response.StatusCode, status)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("administration security headers missing")
	}
	return data, response.Header
}

func (f *workflowFixture) json(t *testing.T, method, path string, value any, status int) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	result, _ := f.request(t, method, path, "application/json", bytes.NewReader(data), status)
	return result
}

func (f *workflowFixture) signIn(t *testing.T) {
	t.Helper()
	data, _ := f.request(t, http.MethodGet, "/api/v1/session", "", nil, http.StatusOK)
	var session struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf"`
	}
	if err := json.Unmarshal(data, &session); err != nil || session.Authenticated || session.CSRF == "" {
		t.Fatal("login session is invalid")
	}
	f.csrf = session.CSRF
	f.json(t, http.MethodPost, "/api/v1/login", map[string]string{"username": "admin", "password": "wrong"}, http.StatusUnauthorized)
	f.json(t, http.MethodPost, "/api/v1/login", map[string]string{"username": "admin", "password": f.password}, http.StatusOK)
	data, _ = f.request(t, http.MethodGet, "/api/v1/session", "", nil, http.StatusOK)
	if err := json.Unmarshal(data, &session); err != nil || !session.Authenticated || session.CSRF == "" {
		t.Fatal("authenticated session is invalid")
	}
	f.csrf = session.CSRF
}

func (f *workflowFixture) waitDiscovery(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		jobs, err := f.app.Catalog.Jobs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) > 0 && jobs[0].State == "complete" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("controlled channel discovery did not finish")
		case <-ticker.C:
		}
	}
}

func TestAuthenticatedAdministrationWorkflow(t *testing.T) {
	f := newWorkflowFixture(t)
	f.request(t, http.MethodGet, "/healthz", "", nil, http.StatusOK)
	f.request(t, http.MethodGet, "/mountz", "", nil, http.StatusServiceUnavailable)
	f.request(t, http.MethodGet, "/readyz", "", nil, http.StatusServiceUnavailable)
	f.request(t, http.MethodGet, "/channels", "", nil, http.StatusSeeOther)
	f.request(t, http.MethodGet, "/api/v1/config", "", nil, http.StatusUnauthorized)
	f.request(t, http.MethodGet, "/login", "", nil, http.StatusOK)
	f.signIn(t)
	f.json(t, http.MethodPost, "/api/v1/channels", map[string]any{"url": "https://www.youtube.com/@controlled", "limit": 50, "revision": f.app.Config.Snapshot().Revision}, http.StatusOK)
	f.waitDiscovery(t)
	for _, path := range []string{"/", "/channels", "/catalogue", "/jobs", "/settings", "/diagnostics", "/fragments/channels", "/fragments/status", "/fragments/jobs", "/static/app.css", "/static/app.js", "/static/htmx.min.js", "/api/v1/config", "/api/v1/channels", "/api/v1/jobs", "/api/v1/jobs/page", "/api/v1/diagnostics", "/api/v1/status", "/api/v1/summary", "/api/v1/videos", "/api/v1/playlists"} {
		f.request(t, http.MethodGet, path, "", nil, http.StatusOK)
	}
	for _, path := range []string{"/missing", "/api/v1/missing", "/assets/artwork?channel=" + workflowChannel + "&scope=show"} {
		f.request(t, http.MethodGet, path, "", nil, http.StatusNotFound)
	}
	for _, path := range []string{"/catalogue?sort=invalid", "/catalogue?q=" + strings.Repeat("a", 201), "/channels?sort=invalid", "/artwork", "/artwork?channel=" + workflowChannel + "&scope=year:2026"} {
		f.request(t, http.MethodGet, path, "", nil, http.StatusBadRequest)
	}
	f.request(t, http.MethodPut, "/channels", "", nil, http.StatusMethodNotAllowed)
	f.json(t, http.MethodPost, "/api/v1/unsupported", nil, http.StatusMethodNotAllowed)
	f.json(t, http.MethodPost, "/api/v1/videos/aqz-KE-bpKQ/prepare", nil, http.StatusOK)
	f.json(t, http.MethodPost, "/api/v1/videos/missing/prepare", nil, http.StatusBadRequest)
	f.json(t, http.MethodPost, "/api/v1/videos/nested/id/prepare", nil, http.StatusBadRequest)
	old := f.csrf
	f.csrf = ""
	f.json(t, http.MethodPost, "/api/v1/videos/aqz-KE-bpKQ/prepare", nil, http.StatusForbidden)
	f.csrf = old
	f.json(t, http.MethodPut, "/api/v1/config", map[string]any{"revision": "stale", "config": f.app.Config.Snapshot().Config}, http.StatusConflict)
	f.request(t, http.MethodPut, "/api/v1/config", "application/json", strings.NewReader(`{"unknown":true}`), http.StatusBadRequest)
	f.request(t, http.MethodPut, "/api/v1/config", "application/json", strings.NewReader(`{} {}`), http.StatusBadRequest)
	f.exercisePolicyAndArtwork(t)
	f.request(t, http.MethodPost, "/logout", "", nil, http.StatusSeeOther)
	f.request(t, http.MethodGet, "/api/v1/status", "", nil, http.StatusUnauthorized)
}

func (f *workflowFixture) exercisePolicyAndArtwork(t *testing.T) {
	t.Helper()
	f.json(t, http.MethodPut, "/api/v1/policy", map[string]any{"channel_id": workflowChannel, "revision": f.app.Config.Snapshot().Revision, "selection": config.Selection{Mode: "all"}, "include_playlists": true}, http.StatusOK)
	if err := f.app.Sync(t.Context(), workflowChannel); err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodGet, "/playlists", "", nil, http.StatusOK)
	f.request(t, http.MethodGet, "/artwork?channel="+workflowChannel+"&scope=playlist:PLfixture0001", "", nil, http.StatusOK)
	var poster bytes.Buffer
	if err := png.Encode(&poster, image.NewRGBA(image.Rect(0, 0, 20, 30))); err != nil {
		t.Fatal(err)
	}
	query := url.Values{"channel": {workflowChannel}, "scope": {"show"}, "revision": {f.app.Config.Snapshot().Revision}}
	f.request(t, http.MethodPost, "/api/v1/artwork?"+query.Encode(), "image/png", bytes.NewReader(poster.Bytes()), http.StatusOK)
	f.request(t, http.MethodGet, "/api/v1/artwork?channel="+workflowChannel+"&scope=show", "", nil, http.StatusOK)
	data, _ := f.request(t, http.MethodGet, "/assets/artwork?channel="+workflowChannel+"&scope=show", "", nil, http.StatusOK)
	size, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || size.Width != 1000 || size.Height != 1500 {
		t.Fatal("saved poster dimensions are invalid", size, err)
	}
	f.request(t, http.MethodPost, "/api/v1/artwork?"+query.Encode(), "image/png", bytes.NewReader(poster.Bytes()), http.StatusConflict)
	query.Set("revision", f.app.Config.Snapshot().Revision)
	f.request(t, http.MethodDelete, "/api/v1/artwork?"+query.Encode(), "", nil, http.StatusOK)
	query.Set("revision", f.app.Config.Snapshot().Revision)
	f.request(t, http.MethodPost, "/api/v1/artwork?"+query.Encode(), "image/png", strings.NewReader("corrupt source image"), http.StatusBadRequest)
	f.request(t, http.MethodGet, "/artwork?channel="+workflowChannel, "", nil, http.StatusOK)
	f.json(t, http.MethodPut, "/api/v1/policy", map[string]any{"channel_id": workflowChannel, "playlist_id": "PLfixture0001", "revision": f.app.Config.Snapshot().Revision, "selection": config.Selection{Mode: "skip"}}, http.StatusOK)
	f.json(t, http.MethodPost, "/api/v1/channels/"+workflowChannel+"/sync", nil, http.StatusOK)
}
