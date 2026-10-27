package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"duplex/internal/app"
	"duplex/internal/catalog"
	"duplex/internal/config"
)

func TestSelectionPreviewAndSaveSharePolicyAuthority(t *testing.T) {
	f := newWorkflowFixture(t)
	f.signIn(t)
	f.json(t, http.MethodPost, "/api/v1/channels", map[string]any{"url": "https://www.youtube.com/@controlled", "limit": 50, "revision": f.app.Config.Snapshot().Revision}, http.StatusOK)
	f.waitDiscovery(t)
	revision := f.app.Config.Snapshot().Revision
	change := app.SelectionChange{ChannelID: workflowChannel, Revision: revision, Selection: config.Selection{Mode: "latest", Limit: 50}, Content: &config.ContentPolicy{Uploads: config.Selection{Mode: "latest", Limit: 1}, Shorts: config.Selection{Mode: "skip"}, Replays: config.Selection{Mode: "skip"}}, VideoOverrides: map[string]string{"aqz-KE-bpKQ": "keep"}}
	body := f.json(t, http.MethodPost, "/api/v1/selection/preview", change, http.StatusOK)

	var preview catalog.SelectionPreview
	if err := json.Unmarshal(body, &preview); err != nil || preview.Selected != 1 || len(preview.Items) != 1 {
		t.Fatal(string(body), err)
	}

	if f.app.Config.Snapshot().Revision != revision {
		t.Fatal("preview saved its draft")
	}

	f.json(t, http.MethodPost, "/api/v1/selection", change, http.StatusOK)
	f.json(t, http.MethodPost, "/api/v1/selection/preview", change, http.StatusConflict)
	change.Revision = f.app.Config.Snapshot().Revision
	change.Content.Shorts.Mode = "inherit"
	f.json(t, http.MethodPost, "/api/v1/selection/preview", change, http.StatusBadRequest)
	change.Content.Shorts.Mode = "skip"
	change.VideoOverrides = map[string]string{"aqz-KE-bpKQ": "exclude"}
	f.json(t, http.MethodPost, "/api/v1/selection", change, http.StatusOK)

	ch := f.app.Config.Snapshot().Config.Channels[0]
	if ch.Content == nil || ch.VideoOverrides["aqz-KE-bpKQ"] != "exclude" {
		t.Fatal("API rules were not retained")
	}
}

func TestRecordingSelectionFormsRetainDraftsAndRequirePreview(t *testing.T) {
	f := newWorkflowFixture(t)
	f.signIn(t)
	f.json(t, http.MethodPost, "/api/v1/channels", map[string]any{"url": "https://www.youtube.com/@controlled", "limit": 50, "revision": f.app.Config.Snapshot().Revision}, http.StatusOK)
	f.waitDiscovery(t)
	f.request(t, http.MethodGet, "/selection?channel="+workflowChannel, "", nil, http.StatusOK)
	f.request(t, http.MethodGet, "/selection?channel=missing", "", nil, http.StatusBadRequest)
	revision := f.app.Config.Snapshot().Revision
	values := url.Values{"id": {workflowChannel}, "revision": {revision}, "separate": {"on"}, "uploads_mode": {"latest"}, "uploads_limit": {"1"}, "shorts_mode": {"skip"}, "shorts_limit": {"1"}, "replays_mode": {"all"}, "replays_limit": {"1"}, "keep": {"aqz-KE-bpKQ"}}

	body, _ := f.form(t, "/actions/selection-preview", values, http.StatusOK)
	if !strings.Contains(string(body), "Selection preview") || !strings.Contains(string(body), "Save selection") || f.app.Config.Snapshot().Revision != revision {
		t.Fatal("form preview mutated settings or missed its review")
	}

	values.Set("exclude", "aqz-KE-bpKQ")

	body, _ = f.form(t, "/actions/selection-preview", values, http.StatusOK)
	if !strings.Contains(string(body), "both kept and excluded") || !strings.Contains(string(body), "aqz-KE-bpKQ") {
		t.Fatal("conflicting draft was lost")
	}

	values.Del("exclude")
	values.Set("uploads_limit", "invalid")

	body, _ = f.form(t, "/actions/selection-preview", values, http.StatusOK)
	if !strings.Contains(string(body), "positive whole number") {
		t.Fatal("invalid count was accepted")
	}

	video := url.Values{"id": {workflowChannel}, "revision": {revision}, "video": {"aqz-KE-bpKQ"}, "override": {"keep"}}

	body, _ = f.form(t, "/actions/video-selection-preview", video, http.StatusOK)
	if !strings.Contains(string(body), "Save selection") {
		t.Fatal("individual keep did not produce a preview")
	}

	video.Set("override", "invalid")
	f.form(t, "/actions/video-selection-preview", video, http.StatusBadRequest)
	video.Set("override", "automatic")
	f.form(t, "/actions/video-selection-preview", video, http.StatusOK)
	video.Set("video", "missing")
	f.form(t, "/actions/video-selection-preview", video, http.StatusBadRequest)

	change := app.SelectionChange{ChannelID: workflowChannel, Revision: revision, Selection: config.Selection{Mode: "all"}, VideoOverrides: map[string]string{"aqz-KE-bpKQ": "keep"}}

	encoded, err := json.Marshal(change)
	if err != nil {
		t.Fatal(err)
	}

	f.form(t, "/actions/selection-save", url.Values{"change": {string(encoded)}}, http.StatusSeeOther)

	body, _ = f.form(t, "/actions/selection-save", url.Values{"change": {string(encoded)}}, http.StatusOK)
	if !strings.Contains(string(body), "Your draft is retained") || !strings.Contains(string(body), "configuration changed") {
		t.Fatal("stale save lost its draft")
	}

	f.form(t, "/actions/selection-save", url.Values{"change": {"{} {}"}}, http.StatusBadRequest)
	f.form(t, "/actions/selection-save", url.Values{"change": {"invalid"}}, http.StatusBadRequest)
}
