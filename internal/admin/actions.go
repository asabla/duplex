// Package admin serves the authenticated administration panel and JSON API.
package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"duplex/internal/config"
)

func (s *Server) actions(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "POST" {
		return false
	}
	switch r.URL.Path {
	case "/actions/policy":
		limit, err := wholeNumber(r, "limit", 1, int(^uint(0)>>1))
		mode := r.FormValue("mode")
		if mode != "latest" {
			limit = 0
			err = nil
		}
		if err == nil {
			err = s.App.UpdatePolicy(r.FormValue("id"), r.FormValue("playlist"), r.FormValue("revision"), config.Selection{Mode: mode, Limit: limit}, r.FormValue("include_playlists") == "on")
		}
		s.result(w, r, err, "Selection saved. A channel refresh is queued when following is enabled.")
	case "/actions/prepare":
		s.result(w, r, s.App.PrepareNow(r.Context(), r.FormValue("id")), "Recording moved to the front of the preparation queue.")
	case "/actions/settings":
		s.result(w, r, s.saveSettings(r), "Settings saved.")
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
	settings := []struct {
		name        string
		destination *int
		max         int
	}{{"cache_gib", &c.CacheGiB, 1048576}, {"refresh_minutes", &c.RefreshMinutes, 10080}, {"max_height", &c.MaxHeight, 2160}}
	for _, field := range settings {
		minValue := 1
		if field.name == "max_height" {
			minValue = 144
		}
		value, err := wholeNumber(r, field.name, minValue, field.max)
		if err != nil {
			return err
		}
		*field.destination = value
	}
	c.Plex.URL = r.FormValue("plex_url")
	c.Plex.Section = r.FormValue("plex_section")
	return s.App.Config.Save(c, r.FormValue("revision"))
}

func (s *Server) artworkUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20+64<<10)
	if err := r.ParseMultipartForm(1 << 20); err != nil { //nolint:gosec // G120: MaxBytesReader immediately above bounds the complete multipart request.
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
		s.result(w, r, fieldFailure{"image", "Select a JPEG or PNG image."}, "")
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
		returnURL := actionReturnURL(r)
		current, parseErr := url.Parse(returnURL)
		if parseErr == nil && current.Path == "/artwork" && current.Query().Get("channel") == r.FormValue("id") && (current.Query().Get("scope") == r.FormValue("scope") || current.Query().Get("scope") == "" && r.FormValue("scope") == "show") {
			location = returnURL
		}
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", location)
		} else {
			http.Redirect(w, r, location, http.StatusSeeOther) // #nosec G710 -- Both paths are restricted to the matched same-origin artwork scope.
			return
		}
	}
	s.result(w, r, err, message)
}

// actionReturnURL accepts only an administration page on this server. The
// supplied query retains filters without permitting an external redirect.
func actionReturnURL(r *http.Request) string {
	location := r.Header.Get("HX-Current-URL")
	if location == "" {
		location = r.Referer()
	}
	candidate, err := url.Parse(location)
	if err == nil && candidate.User == nil && (candidate.Host == "" || candidate.Host == r.Host) && (candidate.Scheme == "" || candidate.Scheme == "http" || candidate.Scheme == "https") && len(candidate.RawQuery) <= 8192 {
		switch candidate.Path {
		case "/channels", "/channel", "/follow", "/video", "/playlists", "/catalogue", "/jobs", "/settings", "/artwork", "/diagnostics":
			return candidate.RequestURI()
		}
	}
	switch r.URL.Path {
	case "/actions/settings", "/actions/config":
		return "/settings"
	case "/actions/prepare":
		return "/catalogue"
	default:
		return "/channels"
	}
}
