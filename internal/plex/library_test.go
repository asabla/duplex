package plex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScansRequireVerifiedTrashPreference(t *testing.T) {
	for _, setting := range []string{"", `<Setting id="autoEmptyTrash" value="1"/>`, `<Setting id="autoEmptyTrash" value="0"/>`} {
		t.Run(setting, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/library/sections/2/prefs" {
					w.Write([]byte(`<MediaContainer><Setting id="useLocalAssets" value="true"/><Setting id="useSeasonTitles" value="true"/><Setting id="enableBIFGeneration" value="false"/><Setting id="enableIntroMarkerGeneration" value="false"/><Setting id="enableCreditsMarkerGeneration" value="false"/><Setting id="enableLoudnessAnalysis" value="false"/></MediaContainer>`))
					return
				}
				if r.URL.Path == "/library/sections" {
					w.Write([]byte(`<MediaContainer><Directory key="2" agent="tv.plex.agents.nfo.series"/></MediaContainer>`))
					return
				}
				w.Write([]byte(`<MediaContainer>` + setting + `</MediaContainer>`))
			}))
			defer server.Close()
			client, _ := New(server.URL, "")
			err := client.LibraryHealth(context.Background(), "2")
			if (err == nil) != (setting == `<Setting id="autoEmptyTrash" value="0"/>`) {
				t.Fatal(err)
			}
		})
	}
}

func TestOptionalAnalysisMustBeDisabled(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/library/sections":
				w.Write([]byte(`<MediaContainer><Directory key="2" agent="tv.plex.agents.nfo.series"/></MediaContainer>`))
			case "/:/prefs":
				w.Write([]byte(`<MediaContainer><Setting id="autoEmptyTrash" value="0"/></MediaContainer>`))
			default:
				w.Write([]byte(`<MediaContainer><Setting id="useLocalAssets" value="true"/><Setting id="useSeasonTitles" value="true"/><Setting id="enableBIFGeneration" value="false"/><Setting id="enableIntroMarkerGeneration" value="` + value + `"/><Setting id="enableCreditsMarkerGeneration" value="false"/><Setting id="enableLoudnessAnalysis" value="false"/></MediaContainer>`))
			}
		}))
		client, _ := New(server.URL, "")
		err := client.LibraryHealth(context.Background(), "2")
		server.Close()
		if (err == nil) != (value == "false") {
			t.Fatal(value, err)
		}
	}
}
