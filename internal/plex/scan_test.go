package plex

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScanChannelsUsesServerRootAndOneRefresh(t *testing.T) {
	for _, scenario := range []struct {
		name, locations string
		channels        []string
		wanted          string
	}{
		{"one", `<Location path="/media/duplex library"/>`, []string{"owner"}, "/media/duplex library/Channel-owner"},
		{"batch", `<Location path="/media"/>`, []string{"one", "two"}, ""},
		{"unknown root", "", []string{"owner"}, ""},
		{"multiple roots", `<Location path="/one"/><Location path="/two"/>`, []string{"owner"}, ""},
		{"non-posix root", `<Location path="relative"/>`, []string{"owner"}, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			refreshes := 0

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/library/sections":
					writeTestResponse(t, w, []byte(`<MediaContainer><Directory key="7">`+scenario.locations+`</Directory></MediaContainer>`))
				case "/library/sections/7/refresh":
					refreshes++

					if r.URL.Query().Get("path") != scenario.wanted || r.Method != http.MethodGet {
						t.Error("unexpected scan scope", r.URL)
					}
				default:
					t.Error("unexpected request", r.URL.Path)
				}
			}))
			defer server.Close()

			client, err := New(server.URL, "")
			if err != nil {
				t.Fatal(err)
			}

			counts := map[string]int64{}
			client.Count = func(name string, n int64) { counts[name] += n }

			if err = client.ScanChannels(t.Context(), "7", scenario.channels); err != nil || refreshes != 1 {
				t.Fatal(refreshes, err)
			}

			if counts["plex.scan.requests"] != 1 || counts["plex.scan.accepted"] != 1 {
				t.Fatal("scan counters omitted request outcome", counts)
			}

			kind := "plex.scan.section"
			if scenario.wanted != "" {
				kind = "plex.scan.scoped"
			}

			if counts[kind] != 1 {
				t.Fatal("scan scope was not counted", counts)
			}

			for _, bad := range []string{"", "../other", "one/two", "one\\two"} {
				if err = client.ScanChannels(t.Context(), "7", []string{bad}); err == nil {
					t.Fatal("unsafe scope accepted", bad)
				}
			}

			if err = client.ScanChannels(t.Context(), "invalid", nil); err == nil {
				t.Fatal("invalid section accepted")
			}
		})
	}
}

func TestScanTransportFailureIsUncertainAndRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("cannot interrupt fixture connection")
			return
		}

		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}

		if err = connection.Close(); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, "fixture-secret")
	if err != nil {
		t.Fatal(err)
	}

	if err = client.Scan(t.Context(), "7"); !errors.Is(err, ErrRequestUncertain) || err.Error() != ErrRequestUncertain.Error() {
		t.Fatal("transport failure lost its bounded class", err)
	}
}
