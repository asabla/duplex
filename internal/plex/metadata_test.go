package plex

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNFOIdentityKeepsCanonicalAndPlaylistHistorySeparate(t *testing.T) {
	for _, tc := range []struct{ identity, source, occurrence string }{
		{"youtube-video:aqz-KE-bpKQ", "aqz-KE-bpKQ", "youtube-video:aqz-KE-bpKQ"},
		{"youtube-playlist:owner:PLfixture0001:aqz-KE-bpKQ", "", "youtube-playlist:owner:PLfixture0001:aqz-KE-bpKQ"},
		{"youtube-channel:owner", "", ""},
	} {
		guid := "tv.plex.agents.nfo.series://episode/" + url.PathEscape(tc.identity)
		if SourceID(guid) != tc.source || OccurrenceID(guid) != tc.occurrence {
			t.Fatal(guid, SourceID(guid), OccurrenceID(guid))
		}
	}
	for _, guid := range []string{"%", "https://episode/youtube-video:one", "tv.plex.agents.nfo.series://show/youtube-video:one", "tv.plex.agents.nfo.series://episode/other:one", "tv.plex.agents.nfo.series://episode/%xx"} {
		if SourceID(guid) != "" || OccurrenceID(guid) != "" {
			t.Fatal("unrelated or malformed GUID accepted", guid)
		}
	}
}

func TestShowRefreshTargetsTheMatchingOwnedIdentity(t *testing.T) {
	var listing string
	var refreshed, scans int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "fixture-secret" || strings.Contains(r.URL.String(), "fixture-secret") {
			t.Error("credential transport boundary violated")
		}
		switch r.URL.Path {
		case "/library/sections/2/all":
			writeTestResponse(t, w, []byte(listing))
		case "/library/metadata/42/refresh":
			if r.Method != http.MethodPut {
				t.Error("metadata refresh must use PUT")
			}
			refreshed++
		case "/library/sections/2/refresh":
			scans++
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(server.URL, "fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	listing = `<MediaContainer><Directory ratingKey="10" guid="%"/><Directory ratingKey="11" guid="plex://show/other"/><Directory ratingKey="12" guid="tv.plex.agents.nfo.series://show/youtube-channel%3Aother"/><Directory ratingKey="42" guid="tv.plex.agents.nfo.series://show/youtube-channel%3Aowner"/></MediaContainer>`
	if err = client.RefreshShow(t.Context(), "2", "owner"); err != nil || refreshed != 1 {
		t.Fatal(refreshed, err)
	}
	if err = client.RefreshShow(t.Context(), "2", "not-scanned"); err != nil || refreshed != 1 {
		t.Fatal("unrelated show refreshed", err)
	}
	listing = `<MediaContainer><Directory ratingKey="invalid" guid="tv.plex.agents.nfo.series://show/youtube-channel%3Aowner"/></MediaContainer>`
	if err = client.RefreshShow(t.Context(), "2", "owner"); err == nil {
		t.Fatal("invalid Plex key accepted")
	}
	if err = client.RefreshShow(t.Context(), "../2", "owner"); err == nil {
		t.Fatal("unsafe section accepted")
	}
	if err = client.Scan(t.Context(), "2"); err != nil || scans != 1 {
		t.Fatal(scans, err)
	}
	if err = client.Scan(t.Context(), "../2"); err == nil {
		t.Fatal("unsafe scan section accepted")
	}
	listing = `<broken`
	if err = client.RefreshShow(t.Context(), "2", "owner"); err == nil {
		t.Fatal("malformed metadata ignored")
	}
}

func TestPlexRequestsRejectRedirectsAndRedactServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://example.invalid/private", http.StatusTemporaryRedirect)
			return
		}
		http.Error(w, "secret-private-signed-url", http.StatusForbidden)
	}))
	defer server.Close()
	client, err := New(server.URL, "fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/redirect", "/denied", "https://other.invalid/private", "%"} {
		if err = client.get(t.Context(), path, nil); err == nil || strings.Contains(err.Error(), "secret-private") || strings.Contains(err.Error(), "example.invalid") {
			t.Fatal("unsafe request failure", err)
		}
	}
}
