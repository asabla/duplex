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
