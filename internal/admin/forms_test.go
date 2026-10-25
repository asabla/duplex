package admin

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func (f *workflowFixture) form(t *testing.T, path string, values url.Values, status int) ([]byte, http.Header) {
	t.Helper()
	values.Set("csrf", f.csrf)
	return f.request(t, http.MethodPost, path, "application/x-www-form-urlencoded", strings.NewReader(values.Encode()), status)
}

func TestHTMLFormsUseTheSameAuthorityAndValidationAsJSON(t *testing.T) {
	f := newWorkflowFixture(t)
	data, _ := f.request(t, http.MethodGet, "/api/v1/session", "", nil, http.StatusOK)
	var session struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatal(err)
	}
	f.csrf = session.CSRF
	f.form(t, "/login", url.Values{"username": {"admin"}, "password": {strings.Repeat("a", 1025)}}, http.StatusBadRequest)
	f.form(t, "/login", url.Values{"username": {"admin"}, "password": {"wrong"}}, http.StatusUnauthorized)
	f.form(t, "/login", url.Values{"username": {"admin"}, "password": {f.password}}, http.StatusSeeOther)
	data, _ = f.request(t, http.MethodGet, "/api/v1/session", "", nil, http.StatusOK)
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatal(err)
	}
	f.csrf = session.CSRF
	f.hx = true
	values := url.Values{"url": {"https://www.youtube.com/@controlled"}, "limit": {"0"}, "revision": {f.app.Config.Snapshot().Revision}}
	f.form(t, "/actions/follow", values, http.StatusBadRequest)
	values.Set("limit", "50")
	values.Set("include_playlists", "on")
	_, headers := f.form(t, "/actions/follow", values, http.StatusOK)
	if headers.Get("HX-Trigger") != "channelsChanged" {
		t.Fatal("successful follow did not announce the catalogue change")
	}
	f.waitDiscovery(t)
	f.form(t, "/actions/sync", url.Values{"id": {workflowChannel}}, http.StatusOK)
	f.form(t, "/actions/prepare", url.Values{"id": {"aqz-KE-bpKQ"}}, http.StatusOK)
	f.form(t, "/actions/prepare", url.Values{"id": {"missing"}}, http.StatusBadRequest)
	f.form(t, "/actions/policy", url.Values{"id": {workflowChannel}, "mode": {"latest"}, "limit": {"bad"}, "revision": {f.app.Config.Snapshot().Revision}}, http.StatusBadRequest)
	f.form(t, "/actions/policy", url.Values{"id": {workflowChannel}, "mode": {"all"}, "include_playlists": {"on"}, "revision": {f.app.Config.Snapshot().Revision}}, http.StatusOK)
	settings := url.Values{"cache_gib": {"1"}, "refresh_minutes": {"30"}, "max_height": {"2160"}, "revision": {f.app.Config.Snapshot().Revision}}
	f.form(t, "/actions/settings", settings, http.StatusOK)
	if f.app.Config.Snapshot().Config.CacheGiB != 1 {
		t.Fatal("structured settings did not reach YAML authority")
	}
	settings.Set("cache_gib", "invalid")
	f.form(t, "/actions/settings", settings, http.StatusBadRequest)
	f.form(t, "/actions/config", url.Values{"yaml": {"invalid: ["}, "revision": {f.app.Config.Snapshot().Revision}}, http.StatusBadRequest)
	text, err := yaml.Marshal(f.app.Config.Snapshot().Config)
	if err != nil {
		t.Fatal(err)
	}
	f.form(t, "/actions/config", url.Values{"yaml": {string(text)}, "revision": {f.app.Config.Snapshot().Revision}}, http.StatusOK)
	f.uploadFormPoster(t)
	f.form(t, "/actions/remove", url.Values{"id": {workflowChannel}, "revision": {"stale"}}, http.StatusBadRequest)
	f.form(t, "/actions/remove", url.Values{"id": {workflowChannel}, "revision": {f.app.Config.Snapshot().Revision}}, http.StatusOK)
	if len(f.app.Config.Snapshot().Config.Channels) != 0 {
		t.Fatal("unfollow did not update YAML")
	}
}

func (f *workflowFixture) uploadFormPoster(t *testing.T) {
	t.Helper()
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 20, 30))); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{"id": workflowChannel, "scope": "show", "revision": f.app.Config.Snapshot().Revision} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("image", "controlled.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(imageData.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	_, headers := f.request(t, http.MethodPost, "/actions/artwork", writer.FormDataContentType(), &body, http.StatusOK)
	if !strings.HasPrefix(headers.Get("HX-Redirect"), "/artwork?") {
		t.Fatal("artwork upload did not return to its preview")
	}
	f.form(t, "/actions/artwork/reset", url.Values{"id": {workflowChannel}, "scope": {"show"}, "revision": {f.app.Config.Snapshot().Revision}}, http.StatusOK)
	f.hx = false
	_, headers = f.form(t, "/actions/artwork/reset", url.Values{"id": {workflowChannel}, "scope": {"show"}, "revision": {f.app.Config.Snapshot().Revision}}, http.StatusSeeOther)
	if !strings.HasPrefix(headers.Get("Location"), "/artwork?") {
		t.Fatal("native form did not return to the artwork preview")
	}
	f.hx = true
	f.request(t, http.MethodPost, "/actions/artwork", "multipart/form-data; boundary=invalid", strings.NewReader("invalid body"), http.StatusBadRequest)
}
