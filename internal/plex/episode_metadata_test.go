package plex

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEpisodeMetadataRefreshVerifiesItsTarget(t *testing.T) {
	section, kind, guid := "7", "episode", "tv.plex.agents.nfo.series://episode/youtube-video%3Afixture"
	failure := false
	refreshes := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "fixture-token" {
			t.Error("token header missing")
		}

		if r.Method == http.MethodPut && r.URL.Path == "/library/metadata/42/refresh" {
			refreshes++
			return
		}

		if failure {
			http.Error(w, "private response", http.StatusInternalServerError)
			return
		}

		if _, err := fmt.Fprintf(w, `<MediaContainer><Video librarySectionID="%s" type="%s" guid="%s"/></MediaContainer>`, section, kind, guid); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}

	if err = client.RefreshEpisode(t.Context(), "7", "42", "youtube-video:fixture"); err != nil || refreshes != 1 {
		t.Fatal(refreshes, err)
	}

	for _, target := range [][3]string{{"../7", "42", "youtube-video:fixture"}, {"7", "../42", "youtube-video:fixture"}, {"7", "42", ""}, {"8", "42", "youtube-video:fixture"}, {"7", "42", "youtube-video:other"}} {
		if err = client.RefreshEpisode(t.Context(), target[0], target[1], target[2]); err == nil {
			t.Fatal("invalid target accepted", target)
		}
	}

	kind = "movie"

	if err = client.RefreshEpisode(t.Context(), "7", "42", "youtube-video:fixture"); err == nil {
		t.Fatal("wrong item type accepted")
	}

	kind, guid = "episode", ""

	if err = client.RefreshEpisode(t.Context(), "7", "42", "youtube-video:fixture"); err == nil {
		t.Fatal("missing identity accepted")
	}

	failure = true

	if err = client.RefreshEpisode(t.Context(), "7", "42", "youtube-video:fixture"); err == nil || refreshes != 1 {
		t.Fatal("failed verification allowed a refresh", refreshes, err)
	}
}
