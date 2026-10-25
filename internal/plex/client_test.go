package plex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVersionDoesNotImplyNFO(t *testing.T) {
	for _, hasNFO := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "present"}[hasNFO], func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Plex-Token") != "secret" {
					t.Error("missing token header")
				}
				if r.URL.Query().Get("X-Plex-Token") != "" {
					t.Error("token leaked into URL")
				}
				switch r.URL.Path {
				case "/identity":
					writeTestResponse(t, w, []byte(`<MediaContainer version="1.43.5.11029-test"/>`))
				case "/media/providers/metadata":
					if hasNFO {
						writeTestResponse(t, w, []byte(`<MediaContainer><MetadataAgentProvider identifier="tv.plex.agents.nfo.series" title="Plex NFO Series"><MetadataType type="2"/></MetadataAgentProvider></MediaContainer>`))
					} else {
						writeTestResponse(t, w, []byte(`<MediaContainer/>`))
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer s.Close()
			c, err := New(s.URL, "secret")
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.Check(context.Background())
			if hasNFO {
				if err != nil || !result.NFOSeries {
					t.Fatal(result, err)
				}
			} else {
				if !errors.Is(err, ErrMissingNFO) || result.NFOSeries {
					t.Fatal(result, err)
				}
			}
		})
	}
}

func TestOriginValidation(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "http://user:secret@localhost", "http://localhost/path", "http://localhost?token=secret"} {
		if _, err := New(u, ""); err == nil {
			t.Fatal("accepted invalid origin")
		}
	}
}

func writeTestResponse(t *testing.T, w http.ResponseWriter, data []byte) {
	t.Helper()
	if _, err := w.Write(data); err != nil {
		t.Error(err)
	}
}
