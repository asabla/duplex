package admin

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestRenderedMarkupFixtures(t *testing.T) {
	if os.Getenv("DUPLEX_MARKUP_TEST") != "1" {
		t.Skip("requires DUPLEX_MARKUP_TEST=1 for rendered HTML validation")
	}

	directory := filepath.Join("..", "..", ".cache", "markup")

	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	fixture := newWorkflowFixture(t)
	fixture.signIn(t)

	export := func(name, path string) {
		body, _ := fixture.request(t, http.MethodGet, path, "", nil, http.StatusOK)
		if err := os.WriteFile(filepath.Join(directory, name+".html"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ name, path string }{
		{"channels-empty", "/channels"},
		{"videos-empty", "/catalogue"},
		{"playlists-empty", "/playlists"},
		{"jobs-empty", "/jobs"},
	} {
		export(item.name, item.path)
	}

	fixture.json(t, http.MethodPost, "/api/v1/channels", map[string]any{"url": "https://www.youtube.com/@controlled", "limit": 50, "revision": fixture.app.Config.Snapshot().Revision}, http.StatusOK)
	fixture.waitDiscovery(t)

	for _, item := range []struct{ name, path string }{
		{"login", "/login"},
		{"channels", "/channels"},
		{"follow", "/follow"},
		{"channel", "/channel?channel=" + workflowChannel},
		{"channel-settings", "/channel?channel=" + workflowChannel + "&tab=settings"},
		{"videos", "/catalogue"},
		{"video", "/video?id=aqz-KE-bpKQ&channel=" + workflowChannel},
		{"playlists", "/playlists"},
		{"jobs", "/jobs"},
		{"settings", "/settings"},
		{"diagnostics", "/diagnostics"},
		{"artwork", "/artwork?channel=" + workflowChannel},
		{"fragment-channels", "/fragments/channels"},
		{"fragment-videos", "/fragments/catalogue"},
		{"fragment-channel", "/fragments/channel?channel=" + workflowChannel},
		{"fragment-status", "/fragments/status"},
		{"fragment-jobs", "/fragments/jobs"},
		{"fragment-playlists", "/fragments/playlist-options"},
	} {
		export(item.name, item.path)
	}

	fixture.hx = true
	form := policyFormID(workflowChannel, "")

	body, _ := fixture.form(t, "/actions/policy", url.Values{"id": {workflowChannel}, "mode": {"latest"}, "limit": {"invalid"}, "_form": {form}, "revision": {fixture.app.Config.Snapshot().Revision}}, http.StatusBadRequest)
	if err := os.WriteFile(filepath.Join(directory, "fragment-field-error.html"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	body, _ = fixture.form(t, "/actions/policy", url.Values{"id": {workflowChannel}, "mode": {"all"}, "_form": {form}, "revision": {fixture.app.Config.Snapshot().Revision}}, http.StatusOK)
	if err := os.WriteFile(filepath.Join(directory, "fragment-saved-policy.html"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}
