package plex

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestBindingsUseBoundedPagesAndStopOnConsumerFailure(t *testing.T) {
	calls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, err := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		if err != nil {
			t.Error(err)
		}

		count, err := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Size"))
		if err != nil || count != 100 {
			t.Error("unbounded request", count, err)
		}

		calls++

		var response strings.Builder
		fmt.Fprintf(&response, `<MediaContainer offset="%d" totalSize="245">`, start)

		for n := start; n < min(start+count, 245); n++ {
			fmt.Fprintf(&response, `<Video ratingKey="%d" guid="tv.plex.agents.nfo.series://episode/youtube-video:video%06d"/>`, n, n)
		}

		response.WriteString(`</MediaContainer>`)
		writeTestResponse(t, w, []byte(response.String()))
	}))
	defer server.Close()

	client, err := New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}

	total := 0

	err = client.WalkBindings(t.Context(), "5", func(items []Binding) error {
		if len(items) > 100 {
			t.Error("page exceeds bound")
		}

		total += len(items)

		return nil
	})
	if err != nil || total != 245 || calls != 3 {
		t.Fatal(total, calls, err)
	}

	sentinel := errors.New("consumer failure")

	calls = 0
	if err = client.WalkBindings(t.Context(), "5", func([]Binding) error { return sentinel }); !errors.Is(err, sentinel) || calls != 1 {
		t.Fatal(calls, err)
	}

	items, err := client.Bindings(t.Context(), "5")
	if err != nil || len(items) != 245 {
		t.Fatal(len(items), err)
	}

	if err = client.WalkBindings(t.Context(), "invalid", func([]Binding) error { return nil }); err == nil {
		t.Fatal("invalid section accepted")
	}
}

func TestBindingsRejectIgnoredPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestResponse(t, w, []byte(`<MediaContainer offset="0" totalSize="1000">`+strings.Repeat(`<Video/>`, 100)+`</MediaContainer>`))
	}))
	defer server.Close()

	client, err := New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}

	if err = client.WalkBindings(t.Context(), "5", func([]Binding) error { return nil }); err == nil {
		t.Fatal("server ignored the next page offset")
	}
}
