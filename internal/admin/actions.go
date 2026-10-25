// Package admin serves the authenticated administration panel and JSON API.
package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"duplex/internal/config"
)

func (s *Server) actions(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "POST" {
		return false
	}
	switch r.URL.Path {
	case "/actions/policy":
		limit, err := strconv.Atoi(r.FormValue("limit"))
		mode := r.FormValue("mode")
		if mode != "latest" {
			limit = 0
			err = nil
		}
		if err == nil {
			err = s.App.UpdatePolicy(r.FormValue("id"), r.FormValue("playlist"), r.FormValue("revision"), config.Selection{Mode: mode, Limit: limit}, r.FormValue("include_playlists") == "on")
		}
		s.result(w, r, err, "Policy saved. Refresh queued. Reload before another settings edit.")
	case "/actions/prepare":
		s.result(w, r, s.App.PrepareNow(r.Context(), r.FormValue("id")), "Recording moved to the front of the preparation queue.")
	case "/actions/settings":
		s.result(w, r, s.saveSettings(r), "Settings saved. Reload before another edit.")
	case "/actions/artwork":
		s.artworkUpload(w, r)
	case "/actions/artwork/reset":
		err := s.App.ResetArtwork(r.Context(), r.FormValue("id"), r.FormValue("scope"), r.FormValue("revision"))
		s.artworkResult(w, r, err, "Automatic artwork restored.")
	default:
		return false
	}
	return true
}

func (s *Server) saveSettings(r *http.Request) error {
	snapshot := s.App.Config.Snapshot()
	c := snapshot.Config
	settings := map[string]*int{"cache_gib": &c.CacheGiB, "refresh_minutes": &c.RefreshMinutes, "max_height": &c.MaxHeight}
	for field, destination := range settings {
		value, err := strconv.Atoi(r.FormValue(field))
		if err != nil {
			return errors.New("settings require valid whole numbers")
		}
		*destination = value
	}
	c.Plex.URL = r.FormValue("plex_url")
	c.Plex.Section = r.FormValue("plex_section")
	return s.App.Config.Save(c, r.FormValue("revision"))
}

func (s *Server) artworkUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20+64<<10)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		s.result(w, r, errors.New("invalid upload or artwork exceeds 10 MiB"), "")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				slog.Error("artwork upload cleanup failed")
			}
		}
	}()
	file, _, err := r.FormFile("image")
	if err != nil {
		s.result(w, r, errors.New("select a JPEG or PNG image"), "")
		return
	}
	err = s.App.SaveArtwork(r.Context(), r.FormValue("id"), r.FormValue("scope"), r.FormValue("revision"), file)
	err = errors.Join(err, file.Close())
	s.artworkResult(w, r, err, "Artwork saved.")
}

func (s *Server) artworkImage(w http.ResponseWriter, r *http.Request) {
	preview, err := s.App.Artwork(r.Context(), r.URL.Query().Get("channel"), r.URL.Query().Get("scope"))
	if err != nil || preview.Path == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, preview.Path)
}

func (s *Server) artworkResult(w http.ResponseWriter, r *http.Request, err error, message string) {
	if err == nil {
		location := "/artwork?" + url.Values{"channel": {r.FormValue("id")}, "scope": {r.FormValue("scope")}}.Encode()
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", location)
		} else {
			http.Redirect(w, r, location, http.StatusSeeOther)
			return
		}
	}
	s.result(w, r, err, message)
}
