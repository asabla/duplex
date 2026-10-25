package admin

import (
	"net/http"
	"strings"

	"duplex/internal/catalog"
	"duplex/internal/config"
)

func (s *Server) readAPI(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "GET" {
		return false
	}
	var result any
	var err error
	switch r.URL.Path {
	case "/api/v1/videos":
		result, err = s.App.Videos(r.Context(), videoFilter(r))
	case "/api/v1/playlists":
		result, err = s.App.Playlists(r.Context(), playlistFilter(r))
	case "/api/v1/jobs/page":
		result, err = s.App.Catalog.FilterJobs(r.Context(), jobFilter(r))
	case "/api/v1/summary":
		status, counts, failure := s.App.Summary(r.Context())
		err = failure
		result = struct {
			Status any `json:"status"`
			Counts any `json:"counts"`
		}{status, counts}
	case "/api/v1/artwork/seasons":
		result, err = s.App.ArtworkSeasons(r.Context(), catalog.SeasonFilter{Channel: r.URL.Query().Get("channel"), Query: r.URL.Query().Get("q"), Page: number(r, "page"), PerPage: number(r, "per_page")})
	case "/api/v1/artwork":
		result, err = s.App.Artwork(r.Context(), r.URL.Query().Get("channel"), r.URL.Query().Get("scope"))
	default:
		return false
	}
	if err != nil {
		reply(w, 400, map[string]string{"error": "invalid filters or unavailable catalogue"})
		return true
	}
	reply(w, 200, result)
	return true
}

func (s *Server) mutateAPI(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/videos/") && strings.HasSuffix(r.URL.Path, "/prepare"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/videos/"), "/prepare")
		if id == "" || strings.Contains(id, "/") {
			reply(w, http.StatusBadRequest, map[string]string{"error": "invalid recording identity"})
			return true
		}
		s.result(w, r, s.App.PrepareNow(r.Context(), id), "Recording moved to the front of the preparation queue")
	case r.URL.Path == "/api/v1/policy" && r.Method == "PUT":
		var value struct {
			ChannelID        string           `json:"channel_id"`
			PlaylistID       string           `json:"playlist_id"`
			Revision         string           `json:"revision"`
			Selection        config.Selection `json:"selection"`
			IncludePlaylists bool             `json:"include_playlists"`
		}
		if !decode(w, r, &value) {
			return true
		}
		s.result(w, r, s.App.UpdatePolicy(value.ChannelID, value.PlaylistID, value.Revision, value.Selection, value.IncludePlaylists), "Policy saved")
	case r.URL.Path == "/api/v1/artwork" && r.Method == "POST":
		q := r.URL.Query()
		s.result(w, r, s.App.SaveArtwork(r.Context(), q.Get("channel"), q.Get("scope"), q.Get("revision"), r.Body), "Artwork saved")
	case r.URL.Path == "/api/v1/artwork" && r.Method == "DELETE":
		q := r.URL.Query()
		s.result(w, r, s.App.ResetArtwork(r.Context(), q.Get("channel"), q.Get("scope"), q.Get("revision")), "Automatic artwork restored")
	default:
		return false
	}
	return true
}
