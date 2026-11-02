package plex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestActivityReadsAllSessionsWithoutLibraryMutations(t *testing.T) {
	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		if r.Method != http.MethodGet || r.Header.Get("X-Plex-Token") != "fixture-token" || r.URL.RawQuery != "" {
			t.Error("unsafe activity request", r.Method, r.URL.Path)
		}

		switch r.URL.Path {
		case "/status/sessions":
			writeTestResponse(t, w, []byte(`<MediaContainer size="3"><Video librarySectionID="other"><Player state="playing"/></Video><Track><Player state="paused"/></Track><Video><Player state="buffering"/></Video></MediaContainer>`))
		case "/activities":
			writeTestResponse(t, w, []byte(`<MediaContainer size="1"><Activity type="library.update"/></MediaContainer>`))
		case "/library/sections":
			writeTestResponse(t, w, []byte(`<MediaContainer><Directory key="7" refreshing="1"/></MediaContainer>`))
		default:
			t.Error("unexpected activity request", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}

	activity, err := client.Activity(t.Context(), "7")
	if err != nil || activity.Sessions != 3 || !activity.Scanning || activity.Tasks != 1 || requests != 3 {
		t.Fatal(activity, requests, err)
	}

	activity, err = client.Activity(t.Context(), "")
	if err != nil || activity.Sessions != 3 || activity.Scanning || activity.Tasks != 1 || requests != 5 {
		t.Fatal("monitoring without a dedicated library failed", activity, requests, err)
	}

	if _, err = client.Activity(t.Context(), "invalid"); err == nil {
		t.Fatal("invalid section accepted")
	}
}

func TestActivityRejectsIncompleteAndUnavailableResponses(t *testing.T) {
	for _, body := range []string{`<MediaContainer/>`, `<MediaContainer size="-1"/>`, `<MediaContainer size="1"/>`, `<MediaContainer size="0"><Video/></MediaContainer>`, `<Other size="0"/>`, "invalid XML"} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeTestResponse(t, w, []byte(body))
			}))
			defer server.Close()

			client, err := New(server.URL, "")
			if err != nil {
				t.Fatal(err)
			}

			if _, err = client.Activity(t.Context(), ""); err == nil {
				t.Fatal("incomplete status must not admit background work")
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()

	client, err := New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = client.Activity(t.Context(), ""); err == nil {
		t.Fatal("access failure was ignored")
	}

	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	if _, err = client.Activity(ctx, ""); err == nil {
		t.Fatal("activity request ignored cancellation")
	}
}

func TestBackgroundTasksRejectsMalformedAndIncompleteResponses(t *testing.T) {
	for _, body := range []string{`<Other/>`, `invalid XML`, `<MediaContainer size="2"><Activity/></MediaContainer>`, `<MediaContainer size="-1"/>`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeTestResponse(t, w, []byte(body)) }))

		client, err := New(server.URL, "")
		if err != nil {
			t.Fatal(err)
		}

		_, err = client.BackgroundTasks(t.Context())

		server.Close()

		if err == nil {
			t.Fatal("incomplete activity response admitted work", body)
		}
	}
}
