package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/config"
)

func TestPlaybackAvailabilityDoesNotConfuseSelectionQualityAndReadiness(t *testing.T) {
	cases := []struct {
		video catalog.Video
		label string
		retry bool
	}{
		{catalog.Video{Height: 2160, Preparation: "ready"}, "Excluded", false},
		{catalog.Video{Exposed: true, State: "upcoming"}, "Waiting for recording", false},
		{catalog.Video{Exposed: true, State: "active"}, "Waiting for recording", false},
		{catalog.Video{Exposed: true, State: "processing"}, "Waiting for recording", false},
		{catalog.Video{Exposed: true, State: "unavailable"}, "Source unavailable", false},
		{catalog.Video{Exposed: true, State: "recorded", Preparation: "ready", Revision: "verified", Size: 10}, "Prepared", false},
		{catalog.Video{Exposed: true, Preparation: "failed", Height: 2160}, "Needs attention", true},
		{catalog.Video{Exposed: true, Preparation: "queued"}, "Queued", false},
		{catalog.Video{Exposed: true, Preparation: "discovered"}, "Awaiting preparation", true},
		{catalog.Video{Exposed: true}, "Awaiting preparation", true},
	}
	for _, tc := range cases {
		state := playback(tc.video)
		if state.Label != tc.label || state.Retry != tc.retry {
			t.Fatal(tc, state)
		}
	}

	if stageLabel("custom-stage") != "custom stage" || stageLabel("active") != "Live broadcast" {
		t.Fatal("unreadable stage")
	}
}

func TestContextLinksRejectRedirectsAndRetainResults(t *testing.T) {
	for _, raw := range []string{"//evil.test/channels", "https://evil.test/channels", "javascript:/channels", "/actions/remove", "/channels#fragment", "/channels?q=" + strings.Repeat("x", 8193), "%"} {
		if localPageURL(raw, "/channels") != "/channels" {
			t.Fatal(raw)
		}
	}

	back := "/channels?q=one%26two&page=2&per_page=50"
	p := page{Page: "/fragments/catalogue", CurrentURL: "/catalogue?q=kept&page=2", BackURL: back, Channel: config.Channel{ID: workflowChannel}}

	link, err := url.Parse(p.ChannelLink("/playlists"))
	if err != nil || link.Query().Get("back") != back || link.Query().Get("channel") != workflowChannel {
		t.Fatal(link, err)
	}

	video, err := url.Parse(p.VideoLink("video000001"))
	if err != nil || video.Query().Get("back") != p.CurrentURL || video.Query().Get("channel") != workflowChannel {
		t.Fatal(video, err)
	}

	if !strings.HasPrefix(p.ClearURL(), "/catalogue?") || !strings.HasPrefix(p.ChannelRowLink(workflowChannel), "/channel?") {
		t.Fatal(p)
	}

	p.Channel = config.Channel{}
	if p.ClearURL() != "/catalogue" {
		t.Fatal(p.ClearURL())
	}

	p.Filters = url.Values{"page": {"2"}, "per_page": {"50"}}
	if p.Filtered() {
		t.Fatal("paging treated as filter")
	}

	p.Filters.Set("preparation", "failed")

	if !p.Filtered() {
		t.Fatal("filter absent")
	}
}

func TestChannelAndVideoTasksRetainContextAndSavedFormAuthority(t *testing.T) {
	f := newWorkflowFixture(t)
	f.signIn(t)
	f.hx = true
	f.form(t, "/actions/follow", url.Values{"url": {"https://www.youtube.com/@controlled"}, "mode": {"latest"}, "limit": {"50"}, "include_playlists": {"on"}, "_form": {"follow-form"}, "revision": {f.app.Config.Snapshot().Revision}}, http.StatusOK)
	f.waitDiscovery(t)

	for _, path := range []string{"/", "/follow", "/channel?channel=" + workflowChannel, "/channel?channel=" + workflowChannel + "&tab=settings", "/video?id=aqz-KE-bpKQ&channel=" + workflowChannel + "&back=%2Fcatalogue%3Fq%3Dkept", "/catalogue?channel=" + workflowChannel, "/playlists?channel=" + workflowChannel, "/jobs?channel=" + workflowChannel, "/artwork?channel=" + workflowChannel} {
		body, _ := f.request(t, http.MethodGet, path, "", nil, http.StatusOK)
		if strings.Contains(string(body), "page unavailable") {
			t.Fatal(path)
		}
	}

	f.request(t, http.MethodGet, "/channel?channel=missing", "", nil, http.StatusNotFound)

	for _, path := range []string{"/fragments/channel?channel=" + workflowChannel, "/fragments/catalogue?channel=" + workflowChannel, "/fragments/channels", "/fragments/jobs"} {
		f.request(t, http.MethodGet, path, "", nil, http.StatusOK)
	}

	// Only the submitted editor is replaced. Its canonical values and revision
	// come from one configuration snapshot, allowing another save without reload.
	policyID := policyFormID(workflowChannel, "")
	values := url.Values{"id": {workflowChannel}, "mode": {"all"}, "_form": {policyID}, "revision": {f.app.Config.Snapshot().Revision}}

	body, _ := f.form(t, "/actions/policy", values, http.StatusOK)
	if !strings.Contains(string(body), `hx-swap-oob="outerHTML"`) || !strings.Contains(string(body), f.app.Config.Snapshot().Revision) {
		t.Fatal(string(body))
	}

	values.Set("mode", "latest")
	values.Set("limit", "17")
	values.Set("revision", f.app.Config.Snapshot().Revision)
	f.form(t, "/actions/policy", values, http.StatusOK)
	values.Set("limit", "invalid")

	body, _ = f.form(t, "/actions/policy", values, http.StatusBadRequest)
	if !strings.Contains(string(body), `data-field-error="`+policyID+`-limit"`) {
		t.Fatal(string(body))
	}

	values.Set("limit", "50")
	values.Set("revision", "stale")

	body, _ = f.form(t, "/actions/policy", values, http.StatusBadRequest)
	if !strings.Contains(string(body), "configuration changed") || strings.Contains(string(body), `hx-swap-oob`) {
		t.Fatal("stale save changed the editor")
	}

	settings := url.Values{"_form": {"settings-form"}, "cache_gib": {"1"}, "refresh_minutes": {"30"}, "max_height": {"1080"}, "revision": {f.app.Config.Snapshot().Revision}}

	body, _ = f.form(t, "/actions/settings", settings, http.StatusOK)
	requireElement(t, body, "form", map[string]string{"id": "settings-form", "hx-swap-oob": "outerHTML"}, "")

	settings.Set("refresh_minutes", "bad")

	body, _ = f.form(t, "/actions/settings", settings, http.StatusBadRequest)
	if !strings.Contains(string(body), "settings-form-refresh_minutes") {
		t.Fatal(string(body))
	}

	v, err := f.app.Video(t.Context(), "aqz-KE-bpKQ")
	if err != nil || !v.Exposed {
		t.Fatal(v, err)
	}

	if _, err = f.app.Video(t.Context(), "missing"); err == nil {
		t.Fatal("missing video accepted")
	}
}

func TestFormFieldTargetsAreLocalAndBounded(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/actions/settings", strings.NewReader(url.Values{"_form": {"settings-form"}, "cache_gib": {"no"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	_, err := wholeNumber(r, "cache_gib", 1, 100)
	if errorField(r, err) != "settings-form-cache_gib" {
		t.Fatal(err)
	}

	if errorField(r, errors.New("generic failure")) != "" {
		t.Fatal("generic error targets a field")
	}

	r = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/actions/settings", strings.NewReader("_form=bad%3Cscript&cache_gib=no"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if errorField(r, fieldFailure{"cache_gib", "Invalid"}) != "" {
		t.Fatal("unsafe field target accepted")
	}
}

func TestPlaylistSaveUpdatesOnlyItsSummaryAndSkippedCount(t *testing.T) {
	f := newWorkflowFixture(t)
	f.signIn(t)
	f.hx = true
	f.form(t, "/actions/follow", url.Values{"url": {"https://www.youtube.com/@controlled"}, "mode": {"latest"}, "limit": {"50"}, "include_playlists": {"on"}, "revision": {f.app.Config.Snapshot().Revision}}, http.StatusOK)
	f.waitDiscovery(t)

	id := policyFormID(workflowChannel, "PLfixture0001")

	body, _ := f.form(t, "/actions/policy", url.Values{"id": {workflowChannel}, "playlist": {"PLfixture0001"}, "mode": {"skip"}, "_form": {id}, "revision": {f.app.Config.Snapshot().Revision}}, http.StatusOK)
	requireElement(t, body, "span", map[string]string{"id": "selection-" + id, "hx-swap-oob": "outerHTML"}, "Override: Skipped")
	requireElement(t, body, "span", map[string]string{"id": "count-" + id, "hx-swap-oob": "outerHTML"}, "0")

	body, _ = f.request(t, http.MethodGet, "/playlists?channel="+workflowChannel, "", nil, http.StatusOK)
	if !strings.Contains(string(body), "Override: Skipped") {
		t.Fatal("saved override absent")
	}

	p := page{ChannelSettings: map[string]config.Channel{workflowChannel: {Indexing: &config.Selection{Mode: "latest", Limit: 50}, Playlists: &config.PlaylistPolicy{Enabled: true}}}}

	form := p.PolicyForm(workflowChannel, "PLinherit")
	if form.Summary != "Inherited: Latest 50" || form.Mode != "inherit" {
		t.Fatal(form)
	}

	ch := p.ChannelSettings[workflowChannel]
	ch.Playlists.Enabled = false

	p.ChannelSettings[workflowChannel] = ch
	if p.PolicyForm(workflowChannel, "PLinherit").Summary != "Skipped by channel" {
		t.Fatal("channel skip is hidden")
	}

	p.channelContext(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/video?id=recording", nil))

	if p.VideoBackURL != "/catalogue" || p.BackURL != "/channels" {
		t.Fatal(p.VideoBackURL, p.BackURL)
	}
}
