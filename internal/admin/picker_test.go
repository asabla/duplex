package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"duplex/internal/catalog"
)

func TestPickersUseBoundedTitleSearchAndKeepTheCurrentScope(t *testing.T) {
	f := newWorkflowFixture(t)
	f.signIn(t)
	f.json(t, http.MethodPost, "/api/v1/channels", map[string]any{"url": "https://www.youtube.com/@controlled", "limit": 50, "revision": f.app.Config.Snapshot().Revision}, http.StatusOK)
	f.waitDiscovery(t)

	for i := range 60 {
		if err := f.app.Catalog.SavePlaylist(t.Context(), catalog.Playlist{ID: fmt.Sprintf("PLpicker%05d", i), ChannelID: workflowChannel, Title: fmt.Sprintf("Picker collection %02d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	body, _ := f.request(t, http.MethodGet, "/catalogue", "", nil, http.StatusOK)
	if strings.Count(string(body), `value="PLpicker`) > 25 {
		t.Fatal("playlist picker loaded more than one bounded page")
	}

	body, _ = f.request(t, http.MethodGet, "/fragments/playlist-options?playlist_q=Picker+collection+59", "", nil, http.StatusOK)
	if !strings.Contains(string(body), "Picker collection 59") || strings.Contains(string(body), "Picker collection 58") {
		t.Fatal(string(body))
	}

	body, _ = f.request(t, http.MethodGet, "/fragments/playlist-options?playlist_q=missing&playlist=PLpicker00059", "", nil, http.StatusOK)
	if !strings.Contains(string(body), `value="PLpicker00059" selected>Picker collection 59`) {
		t.Fatal("current selection was lost outside the search page", string(body))
	}

	body, _ = f.request(t, http.MethodGet, "/artwork?channel="+workflowChannel+"&scope=playlist:PLpicker00059&q=Picker+collection+01", "", nil, http.StatusOK)
	if !strings.Contains(string(body), "Picker collection 59 (current selection)") || !strings.Contains(string(body), "Picker collection 01") || !strings.Contains(string(body), "Showing 1–1 of 1") {
		t.Fatal("artwork search lost the selected scope or meaningful title")
	}

	body, _ = f.request(t, http.MethodGet, "/api/v1/artwork/seasons?channel="+workflowChannel+"&q=Picker&page=999", "", nil, http.StatusOK)

	var seasons catalog.SeasonPage
	if err := json.Unmarshal(body, &seasons); err != nil || seasons.Total != 60 || seasons.Page != 3 || len(seasons.Items) != 10 {
		t.Fatal(seasons, err)
	}

	f.request(t, http.MethodGet, "/api/v1/artwork/seasons?channel="+workflowChannel+"&q="+strings.Repeat("x", 201), "", nil, http.StatusBadRequest)
	f.request(t, http.MethodGet, "/api/v1/artwork/seasons?channel=unknown", "", nil, http.StatusBadRequest)
	f.request(t, http.MethodGet, "/playlists?availability=missing", "", nil, http.StatusOK)
	f.request(t, http.MethodGet, "/api/v1/playlists?availability=invalid", "", nil, http.StatusBadRequest)
	f.request(t, http.MethodGet, "/fragments/playlist-options?playlist_q="+strings.Repeat("x", 201), "", nil, http.StatusBadRequest)
}
