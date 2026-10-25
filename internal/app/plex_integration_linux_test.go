//go:build linux

package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/diagnostics"
	"duplex/internal/library"
)

// This is a repeatable protocol and mount check, separate from the claimed Plex
// server acceptance gate. The HTTP peer deliberately supplies controlled XML.
func TestMountedPlexProtocolKeepsOrderingFailuresUntilReconciled(t *testing.T) {
	if os.Getenv("DUPLEX_FUSE_TEST") != "1" {
		t.Skip("requires explicit Linux FUSE qualification")
	}
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	a.Measurements = &diagnostics.Recorder{}
	projectionVideo(t, a, "video000000", 1)
	if err := a.Catalog.SavePlaylist(t.Context(), catalog.Playlist{ID: "PLfixture0001", ChannelID: testOwner, Title: "Named season"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Catalog.AddPlaylistMember(t.Context(), "PLfixture0001", "video000000", 1, true); err != nil {
		t.Fatal(err)
	}
	mountpoint := filepath.Join(t.TempDir(), "library")
	mount, err := library.MountProvider(mountpoint, catalogueFS{app: a})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			closeTest(t, mount)
		}
	}()
	a.mount = mount
	var unhealthy atomic.Bool
	var scans, refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "controlled-token" {
			t.Error("token header missing")
		}
		body := "<MediaContainer/>"
		switch r.URL.Path {
		case "/identity":
			body = `<MediaContainer version="1.43.5.11029" claimed="1"/>`
		case "/media/providers/metadata":
			body = `<MediaContainer><MetadataAgentProvider identifier="tv.plex.agents.nfo.series"><MetadataType type="2"/></MetadataAgentProvider></MediaContainer>`
		case "/library/sections":
			body = `<MediaContainer><Directory key="7" agent="tv.plex.agents.nfo.series" refreshing="0"/></MediaContainer>`
		case "/:/prefs":
			body = fmt.Sprintf(`<MediaContainer><Setting id="autoEmptyTrash" value="%t"/></MediaContainer>`, unhealthy.Load())
		case "/library/sections/7/prefs":
			body = `<MediaContainer><Setting id="enableBIFGeneration" value="false"/><Setting id="enableIntroMarkerGeneration" value="false"/><Setting id="enableCreditsMarkerGeneration" value="false"/><Setting id="enableLoudnessAnalysis" value="false"/><Setting id="useLocalAssets" value="true"/><Setting id="useSeasonTitles" value="true"/></MediaContainer>`
		case "/library/sections/7/allLeaves":
			body = fmt.Sprintf(`<MediaContainer offset="0" totalSize="2"><Video ratingKey="50" index="1" guid="tv.plex.agents.nfo.series://episode/youtube-video%%3Avideo000000"/><Video ratingKey="51" index="1" guid="tv.plex.agents.nfo.series://episode/%s"/></MediaContainer>`, url.PathEscape(catalog.PlaylistOccurrenceID(testOwner, "PLfixture0001", "video000000")))
		case "/library/sections/7/refresh":
			hintPath := filepath.Join(mountpoint, "Channel-"+testOwner, "Season 0001", ".plexmatch")
			data, err := os.ReadFile(hintPath) //nolint:gosec // G304: This reads a fixed playlist hint from the temporary qualification mount.
			if err != nil || !strings.Contains(string(data), "ep: S01E01:") {
				t.Error("scan began before playlist hints were published", err, string(data))
			}
			scans.Add(1)
		case "/library/sections/7/all":
			body = fmt.Sprintf(`<MediaContainer><Directory ratingKey="42" guid="tv.plex.agents.nfo.series://show/%s"/></MediaContainer>`, url.PathEscape("youtube-channel:"+testOwner))
		case "/library/metadata/42/refresh":
			refreshes.Add(1)
		default:
			http.NotFound(w, r)
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	tokenFile := filepath.Join(t.TempDir(), "plex-token")
	if err = os.WriteFile(tokenFile, []byte("controlled-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := a.Config.Snapshot()
	settings.Config.Plex = config.Plex{URL: server.URL, Section: "7", TokenFile: tokenFile}
	if err = a.Config.Save(settings.Config, settings.Revision); err != nil {
		t.Fatal(err)
	}
	if err = a.Scan(t.Context()); err != nil || scans.Load() != 1 {
		t.Fatal(scans.Load(), err)
	}
	if err = a.refreshArtwork(t.Context(), testOwner); err != nil || refreshes.Load() != 1 {
		t.Fatal(refreshes.Load(), err)
	}
	if _, err = a.Catalog.RecordShowMetadata(t.Context(), testOwner, "new-show-version", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	job, err := a.Catalog.NextShowRefresh(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a.runShowRefresh(t.Context(), job)
	pending, failure, err := a.Catalog.ShowRefreshStatus(t.Context())
	if err != nil || pending != 0 || failure != "" || refreshes.Load() != 2 {
		t.Fatal(pending, failure, refreshes.Load(), err)
	}
	unhealthy.Store(true)
	if err = a.Scan(t.Context()); err == nil {
		t.Fatal("unsafe trash setting permitted a scan")
	}
	unhealthy.Store(false)
	if err = a.integration(t.Context(), integrationHealth); err != nil {
		t.Fatal(err)
	}
	if a.plexError == "" {
		t.Fatal("health check hid an unreconciled scan failure")
	}
	if err = a.Scan(t.Context()); err != nil || a.plexError != "" {
		t.Fatal("successful reconciliation did not clear the failure", err, a.plexError)
	}
	for _, connection := range []config.Plex{{}, {URL: server.URL, TokenFile: tokenFile + "-missing"}, {URL: "file:///private"}} {
		if _, err = a.plexClientFor(connection); err == nil {
			t.Fatal("invalid connection accepted")
		}
	}
	if err = mount.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	previous := scans.Load()
	if err = a.Scan(t.Context()); err == nil || scans.Load() != previous {
		t.Fatal("scan permitted without a ready mount", err)
	}
	a.mount = nil
	a.integrationResult(integrationBindings, errors.New("controlled failure"))
	if a.plexBindingError == "" {
		t.Fatal("binding failure was not retained")
	}
}
