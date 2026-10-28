package admin

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"duplex/internal/app"
)

func TestOptionalPlexSetupDoesNotHideServiceFailures(t *testing.T) {
	server, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, failure := range []string{"", "configuration", "source", "filesystem", "restart", "plex"} {
		t.Run(failure, func(t *testing.T) {
			state := app.Status{MountReady: true, PlexError: "plex connection is not configured"}

			switch failure {
			case "configuration":
				state.ConfigError = "Invalid configuration"
			case "source":
				state.SourceError = "Source check failed"
			case "filesystem":
				state.MountError = "Filesystem stopped"
			case "restart":
				state.RestartRequired = true
			case "plex":
				state.Config.Config.Plex.URL = "http://plex:32400"
				state.PlexError = "Plex server is unavailable"
			}

			var body bytes.Buffer

			if err := server.templates.ExecuteTemplate(&body, "service-alert", page{Status: state}); err != nil {
				t.Fatal(err)
			}

			if failure == "" {
				requireElement(t, body.Bytes(), "aside", map[string]string{"aria-label": "Plex setup"}, "")
				requireElement(t, body.Bytes(), "a", map[string]string{"href": "/settings#plex-connection"}, "Configure automatic scans")

				if strings.Contains(body.String(), "Service needs attention") {
					t.Fatal("optional integration was presented as a service failure")
				}
			} else {
				requireElement(t, body.Bytes(), "aside", map[string]string{"aria-label": "Service needs attention"}, "")

				if strings.Contains(body.String(), "Connect your Plex library") {
					t.Fatal("setup guidance hid a service failure")
				}
			}

			body.Reset()

			if err := server.templates.ExecuteTemplate(&body, "status", page{Status: state}); err != nil {
				t.Fatal(err)
			}

			if failure == "" {
				requireElement(t, body.Bytes(), "dd", nil, "Filesystem ready")

				if strings.Contains(body.String(), "plex connection is not configured") {
					t.Fatal("optional integration was presented as a diagnostic error")
				}
			} else {
				requireElement(t, body.Bytes(), "dd", nil, "Needs attention")
			}
		})
	}
}

func TestFirstChannelStartsWithUploadsAndExposesRecordingSelection(t *testing.T) {
	f := newWorkflowFixture(t)
	f.signIn(t)

	body, _ := f.request(t, http.MethodGet, "/channels", "", nil, http.StatusOK)
	requireElement(t, body, "strong", nil, "Bring your first channel into Plex")

	body, _ = f.request(t, http.MethodGet, "/follow", "", nil, http.StatusOK)

	checkbox := requireElement(t, body, "input", map[string]string{"name": "include_playlists"}, "")
	if hasAttributes(checkbox, map[string]string{"checked": ""}) {
		t.Fatal("first channel opted into every playlist")
	}

	f.hx = true
	f.form(t, "/actions/follow", url.Values{"url": {"https://www.youtube.com/@controlled"}, "mode": {"latest"}, "limit": {"5"}, "_form": {"follow-form"}, "revision": {f.app.Config.Snapshot().Revision}}, http.StatusOK)
	f.waitDiscovery(t)

	channel := f.app.Config.Snapshot().Config.Channels[0]
	if channel.Playlists != nil && channel.Playlists.Enabled {
		t.Fatal("saved follow settings enabled unselected playlists")
	}

	body, _ = f.request(t, http.MethodGet, "/selection?channel="+workflowChannel, "", nil, http.StatusOK)
	requireElement(t, body, "a", map[string]string{"href": "/channels", "aria-current": "page"}, "Channels")
	requireElement(t, body, "a", map[string]string{"aria-current": "page"}, "Selection")

	settings := requireElement(t, body, "a", map[string]string{"href": "/channel?back=%2Fchannels&channel=" + workflowChannel + "&tab=settings"}, "Settings")
	if hasAttributes(settings, map[string]string{"aria-current": "page"}) {
		t.Fatal("selection page also marked Settings as current")
	}

	body, _ = f.request(t, http.MethodGet, "/channels?q=does-not-match", "", nil, http.StatusOK)
	requireElement(t, body, "strong", nil, "No matching channels")
	requireElement(t, body, "a", map[string]string{"href": "/channels"}, "Clear filters")
}
