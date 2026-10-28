package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"duplex/internal/app"
	"duplex/internal/catalog"
	"duplex/internal/source"
)

func selectionWorkflow(t *testing.T) (*workflowFixture, url.Values) {
	t.Helper()
	f := newWorkflowFixture(t)
	f.signIn(t)
	f.json(t, http.MethodPost, "/api/v1/channels", map[string]any{"url": "https://www.youtube.com/@controlled", "limit": 50, "revision": f.app.Config.Snapshot().Revision}, http.StatusOK)
	f.waitDiscovery(t)

	return f, url.Values{"editor": {"1"}, "id": {workflowChannel}, "revision": {f.app.Config.Snapshot().Revision}, "separate": {"on"}, "uploads_mode": {"all"}, "uploads_limit": {"0"}, "shorts_mode": {"skip"}, "shorts_limit": {"0"}, "replays_mode": {"skip"}, "replays_limit": {"0"}, "keep": {"aqz-KE-bpKQ"}}
}

func reviewedSelection(t *testing.T, f *workflowFixture, values url.Values) string {
	t.Helper()
	body, _ := f.form(t, "/actions/selection-preview", values, http.StatusOK)

	node := requireElement(t, body, "input", map[string]string{"name": "change"}, "")
	for _, attribute := range node.Attr {
		if attribute.Key == "value" {
			return attribute.Val
		}
	}

	t.Fatal("review has no submitted change")

	return ""
}

func TestSelectionSaveRejectsChangedVisibleRulesWithoutJavaScript(t *testing.T) {
	f, values := selectionWorkflow(t)
	revision := f.app.Config.Snapshot().Revision
	values.Set("change", reviewedSelection(t, f, values))
	values.Set("keep", "video000009")

	body, _ := f.form(t, "/actions/selection-save", values, http.StatusOK)
	if f.app.Config.Snapshot().Revision != revision || !strings.Contains(string(body), "rules changed after preview") {
		t.Fatal("an obsolete review was saved", string(body))
	}

	requireElement(t, body, "textarea", map[string]string{"id": "selection-keep"}, "video000009")
	requireElement(t, body, "div", map[string]string{"id": "selection-errors", "data-focus-errors": ""}, "")

	if strings.Contains(string(body), "name=\"change\"") || strings.Contains(string(body), "Open current settings") {
		t.Fatal("outdated save or conflict guidance remained available")
	}

	// Equivalent URL input is compared by normalized rules, not raw form text.
	values.Set("keep", "https://www.youtube.com/watch?v=aqz-KE-bpKQ")

	_, headers := f.form(t, "/actions/selection-save", values, http.StatusSeeOther)
	if !strings.Contains(headers.Get("Location"), "saved=1") {
		t.Fatal("successful save has no confirmation redirect", headers)
	}

	body, _ = f.request(t, http.MethodGet, headers.Get("Location"), "", nil, http.StatusOK)
	if !strings.Contains(string(body), "Selection saved. Channel discovery was queued.") {
		t.Fatal("saved result was not confirmed")
	}
}

func TestSelectionValidationIdentifiesFieldsAndIgnoresInactiveLimits(t *testing.T) {
	f, values := selectionWorkflow(t)
	body, _ := f.form(t, "/actions/selection-preview", values, http.StatusOK)
	requireElement(t, body, "form", map[string]string{"id": "selection-editor", "novalidate": ""}, "")

	if !strings.Contains(string(body), "Save selection") {
		t.Fatal("ignored zero limits blocked preview")
	}

	values.Set("uploads_mode", "latest")
	body, _ = f.form(t, "/actions/selection-preview", values, http.StatusOK)
	requireElement(t, body, "a", map[string]string{"href": "#content-uploads-limit"}, "Enter a positive whole number for Uploads.")
	requireElement(t, body, "input", map[string]string{"id": "content-uploads-limit", "aria-invalid": "true", "aria-describedby": "content-uploads-limit-error"}, "")

	requireElement(t, body, "title", nil, "Error: duplex")

	if strings.Contains(string(body), "Open current settings") {
		t.Fatal("ordinary validation used conflict guidance or missed error title")
	}

	values.Del("separate")
	values.Set("keep", "not-a-recording")
	body, _ = f.form(t, "/actions/selection-preview", values, http.StatusOK)
	requireElement(t, body, "textarea", map[string]string{"id": "selection-keep", "aria-invalid": "true"}, "not-a-recording")
	requireElement(t, body, "a", map[string]string{"href": "#selection-keep"}, "Enter an eleven-character video ID or a YouTube recording URL.")

	values.Set("keep", "https://youtu.be/aqz-KE-bpKQ?t=30")
	values.Set("uploads_mode", "invalid")

	body, _ = f.form(t, "/actions/selection-preview", values, http.StatusOK)
	if !strings.Contains(string(body), "Save selection") {
		t.Fatal("shared selection validated inactive content rules")
	}

	values.Set("separate", "on")
	body, _ = f.form(t, "/actions/selection-preview", values, http.StatusOK)
	requireElement(t, body, "select", map[string]string{"id": "content-uploads-mode", "aria-invalid": "true"}, "")
}

func TestRecordingPickerEditsOnlyTheDraftAndChecksCanonicalOwnership(t *testing.T) {
	f, values := selectionWorkflow(t)
	revision := f.app.Config.Snapshot().Revision

	values.Set("recording_q", "https://youtu.be/aqz-KE-bpKQ")
	values.Set("recording_action", "exclude:aqz-KE-bpKQ")
	body, _ := f.form(t, "/actions/selection-search", values, http.StatusOK)
	requireElement(t, body, "textarea", map[string]string{"id": "selection-keep"}, "")
	requireElement(t, body, "textarea", map[string]string{"id": "selection-exclude"}, "aqz-KE-bpKQ")
	requireElement(t, body, "button", map[string]string{"value": "exclude:aqz-KE-bpKQ", "aria-pressed": "true"}, "Exclude")

	if f.app.Config.Snapshot().Revision != revision || strings.Contains(string(body), "data-selection-save") {
		t.Fatal("picker saved settings or retained an obsolete save")
	}

	values.Set("recording_action", "automatic:aqz-KE-bpKQ")
	body, _ = f.form(t, "/actions/selection-search", values, http.StatusOK)
	requireElement(t, body, "textarea", map[string]string{"id": "selection-keep"}, "")

	if err := f.app.Catalog.SaveMetadata(t.Context(), source.Metadata{ID: "video000009", ChannelID: "foreign", Title: "Foreign recording", Duration: 60}); err != nil {
		t.Fatal(err)
	}

	values.Set("recording_action", "keep:video000009")

	body, _ = f.form(t, "/actions/selection-search", values, http.StatusOK)
	if !strings.Contains(string(body), "Choose a recording discovered for this channel.") || strings.Contains(string(body), "Foreign recording") {
		t.Fatal("foreign ownership was not enforced")
	}

	values.Set("recording_action", "invalid:aqz-KE-bpKQ")
	f.form(t, "/actions/selection-search", values, http.StatusOK)
	values.Set("recording_action", "keep:bad")
	f.form(t, "/actions/selection-search", values, http.StatusOK)
}

func TestRecordingRuleFilterSeparatesExplicitExclusionFromNonselection(t *testing.T) {
	f, values := selectionWorkflow(t)

	change := app.SelectionChange{}
	if err := json.Unmarshal([]byte(reviewedSelection(t, f, values)), &change); err != nil {
		t.Fatal(err)
	}

	change.VideoOverrides = map[string]string{"aqz-KE-bpKQ": "exclude"}
	previewBody := f.json(t, http.MethodPost, "/api/v1/selection/preview?view=removed&page=99", change, http.StatusOK)

	var preview catalog.SelectionPreview
	if err := json.Unmarshal(previewBody, &preview); err != nil || preview.View != "removed" || preview.Page != 1 || preview.Removed != 1 || len(preview.Items) != 1 {
		t.Fatal(string(previewBody), err)
	}

	f.json(t, http.MethodPost, "/api/v1/selection/preview?view=invalid", change, http.StatusBadRequest)
	f.json(t, http.MethodPost, "/api/v1/selection", change, http.StatusOK)

	if err := f.app.Catalog.SaveMetadata(t.Context(), source.Metadata{ID: "video000009", ChannelID: workflowChannel, Title: "Older automatic recording", Duration: 60}); err != nil {
		t.Fatal(err)
	}

	read := func(path string, want int) {
		t.Helper()
		body := f.json(t, http.MethodGet, path, nil, http.StatusOK)

		var result catalog.VideoPage
		if err := json.Unmarshal(body, &result); err != nil || result.Total != want {
			t.Fatal(string(body), err)
		}
	}
	// The excluded channel recording remains selected through its playlist.
	read("/api/v1/videos?inclusion=outside", 1)
	read("/api/v1/videos?inclusion=outside&rule=exclude", 0)
	read("/api/v1/videos?inclusion=all&rule=exclude", 1)
	read("/api/v1/videos?inclusion=all&rule=keep", 0)
	f.request(t, http.MethodGet, "/catalogue?rule=invalid", "", nil, http.StatusBadRequest)

	body, _ := f.request(t, http.MethodGet, "/catalogue?inclusion=all", "", nil, http.StatusOK)
	if !strings.Contains(string(body), "Excluded from channel recordings; selected through a playlist") || !strings.Contains(string(body), "Outside the automatic recording and playlist selections") {
		t.Fatal("selection reasons were not distinguished", string(body))
	}
}

func TestRecordingURLParsingRejectsOtherHostsAndCredentials(t *testing.T) {
	for _, raw := range []string{"video000001", "https://www.youtube.com/watch?v=video000001", "https://youtu.be/video000001", "https://youtube.com/shorts/video000001", "https://m.youtube.com/live/video000001"} {
		if id, err := recordingID(raw); err != nil || id != "video000001" {
			t.Fatal(raw, id, err)
		}
	}

	for _, raw := range []string{"https://foreign.test/watch?v=video000001", "https://secret@youtube.com/watch?v=video000001", "http://youtube.com/watch?v=video000001", "https://youtu.be/video000001/extra", "bad"} {
		if _, err := recordingID(raw); err == nil {
			t.Fatal("invalid recording accepted", raw)
		}
	}
}
