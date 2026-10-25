package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestFragmentLinksRetainFiltersWithoutMutatingThem(t *testing.T) {
	values := url.Values{"q": {"channel & title"}, "failure": {"failed"}, "state": {"paused"}, "sort": {"updated"}, "page": {"99"}, "per_page": {"50"}}
	before := values.Encode()
	for _, path := range []string{"/channels", "/jobs"} {
		p := paginate(values, path, "Rows", 80)
		link, err := url.Parse(p.FragmentURL())
		if err != nil {
			t.Fatal(err)
		}
		if link.Path != "/fragments"+path || link.Query().Get("page") != "2" || link.Query().Get("failure") != "failed" || link.Query().Get("q") != values.Get("q") || link.Query().Get("sort") != "updated" {
			t.Fatal(link)
		}
	}
	if values.Encode() != before {
		t.Fatal("fragment generation changed caller filters")
	}
}

func TestActionReturnURLRejectsExternalAndActionLocations(t *testing.T) {
	for _, tc := range []struct{ location, want string }{
		{"http://duplex.test/channels?q=edited&page=2", "/channels?q=edited&page=2"},
		{"/playlists?channel=owner&policy=all", "/playlists?channel=owner&policy=all"},
		{"https://other.test/channels", "/settings"},
		{"//other.test/channels", "/settings"},
		{"http://user@duplex.test/channels", "/settings"},
		{"javascript:/channels", "/settings"},
		{"/actions/settings", "/settings"},
		{"/channels?q=" + strings.Repeat("x", 8193), "/settings"},
	} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://duplex.test/actions/settings", nil)
		r.Header.Set("HX-Current-URL", tc.location)
		if got := actionReturnURL(r); got != tc.want {
			t.Fatalf("return location: got %q, want %q", got, tc.want)
		}
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://duplex.test/actions/prepare", nil)
	r.Header.Set("Referer", "http://duplex.test/catalogue?page=3")
	if got := actionReturnURL(r); got != "/catalogue?page=3" {
		t.Fatal(got)
	}
}

func TestFormResultsRedirectOrRetainTheCorrectPage(t *testing.T) {
	s, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://duplex.test/actions/policy", nil)
	r.Header.Set("Referer", "http://duplex.test/playlists?page=2&channel=owner")
	w := httptest.NewRecorder()
	s.result(w, r, nil, "Saved")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/playlists?page=2&channel=owner" {
		t.Fatal(w)
	}
	r.Header.Set("HX-Request", "true")
	for _, failure := range []error{nil, errors.New("stale form")} {
		w = httptest.NewRecorder()
		s.result(w, r, failure, "Saved")
		if !strings.Contains(w.Body.String(), `href="/playlists?page=2&amp;channel=owner"`) {
			t.Fatal(w.Body.String())
		}
	}
}

func TestSortARIAReflectsDefaultsAndDirections(t *testing.T) {
	for _, tc := range []struct{ value, want string }{{"", "descending"}, {"newest", "descending"}, {"oldest", "ascending"}, {"title", "none"}} {
		if got := sortARIA(url.Values{"sort": {tc.value}}, "oldest", "newest", "newest"); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}

func TestArtworkReturnRetainsMatchingSeasonSearch(t *testing.T) {
	s, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ current, want string }{
		{"http://duplex.test/artwork?channel=owner&scope=playlist%3APLone&q=Named&page=2", "/artwork?channel=owner&scope=playlist%3APLone&q=Named&page=2"},
		{"http://duplex.test/artwork?channel=other&scope=playlist%3APLone", "/artwork?channel=owner&scope=playlist%3APLone"},
		{"https://other.test/artwork?channel=owner&scope=playlist%3APLone", "/artwork?channel=owner&scope=playlist%3APLone"},
	} {
		values := url.Values{"id": {"owner"}, "scope": {"playlist:PLone"}}
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://duplex.test/actions/artwork/reset", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("HX-Request", "true")
		r.Header.Set("HX-Current-URL", tc.current)
		w := httptest.NewRecorder()
		s.artworkResult(w, r, nil, "Saved")
		if got := w.Header().Get("HX-Redirect"); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}

func TestSortHeadersToggleDirectionAndRetainFilters(t *testing.T) {
	for _, tc := range []struct{ path, current, column, want string }{
		{"/channels", "", "title", "title_desc"},
		{"/channels", "title_desc", "title", "title"},
		{"/playlists", "season", "season", "season_desc"},
		{"/playlists", "updated", "updated", "updated_oldest"},
		{"/catalogue", "", "newest", "oldest"},
		{"/catalogue", "oldest", "newest", "newest"},
		{"/catalogue", "newest", "title", "title"},
	} {
		values := url.Values{"sort": {tc.current}, "page": {"2"}, "q": {"kept"}, "per_page": {"50"}, "channel": {"owner"}}
		link, err := url.Parse(sortLink(values, tc.path, tc.column))
		if err != nil {
			t.Fatal(err)
		}
		if link.Query().Get("sort") != tc.want || link.Query().Has("page") || link.Query().Get("q") != "kept" || link.Query().Get("per_page") != "50" || link.Query().Get("channel") != "owner" {
			t.Fatal(tc, link)
		}
		if values.Get("page") != "2" {
			t.Fatal("sort changed the caller's filters")
		}
	}
}
