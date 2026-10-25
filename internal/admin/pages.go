package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"duplex/internal/catalog"
	"duplex/internal/config"

	"gopkg.in/yaml.v3"
)

func number(r *http.Request, key string) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return 0
	}
	return n
}

func videoFilter(r *http.Request) catalog.VideoFilter {
	q := r.URL.Query()
	inclusion := q.Get("inclusion")
	if inclusion == "" {
		inclusion = "selected"
	}
	return catalog.VideoFilter{Query: strings.TrimSpace(q.Get("q")), Channel: q.Get("channel"), Playlist: q.Get("playlist"), Lifecycle: q.Get("lifecycle"), Preparation: q.Get("preparation"), Inclusion: inclusion, Sort: q.Get("sort"), Page: number(r, "page"), PerPage: number(r, "per_page")}
}

func playlistFilter(r *http.Request) catalog.PlaylistFilter {
	q := r.URL.Query()
	return catalog.PlaylistFilter{Query: q.Get("q"), Channel: q.Get("channel"), Mode: q.Get("policy"), Failure: q.Get("failure"), Availability: q.Get("availability"), Sort: q.Get("sort"), Page: number(r, "page"), PerPage: number(r, "per_page")}
}

func jobFilter(r *http.Request) catalog.JobFilter {
	q := r.URL.Query()
	return catalog.JobFilter{Channel: q.Get("channel"), State: q.Get("state"), Sort: q.Get("sort"), Page: number(r, "page"), PerPage: number(r, "per_page")}
}

func (s *Server) readPage(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/assets/artwork" {
		s.artworkImage(w, r)
		return
	}
	switch path {
	case "/", "/channels", "/playlists", "/catalogue", "/jobs", "/settings", "/diagnostics", "/artwork", "/fragments/channels", "/fragments/status", "/fragments/jobs", "/fragments/playlist-options":
	default:
		http.NotFound(w, r)
		return
	}
	if len(r.URL.RawQuery) > 8192 || len(r.URL.Query().Get("q")) > 200 || len(r.URL.Query().Get("playlist_q")) > 200 {
		http.Error(w, "filter is too long", http.StatusBadRequest)
		return
	}
	status, counts, err := s.App.Summary(r.Context())
	if err != nil {
		http.Error(w, "catalogue unavailable", http.StatusServiceUnavailable)
		return
	}
	session, _ := s.Auth.Session(r)
	v := page{Page: path, CSRF: session.CSRF, Status: status, Revision: status.Config.Revision, Counts: counts, Filters: r.URL.Query(), ChannelSettings: map[string]config.Channel{}}
	for _, ch := range status.Config.Config.Channels {
		v.ChannelSettings[ch.ID] = ch
	}
	if err = s.pageData(r, &v); err != nil {
		http.Error(w, "cannot read this page: invalid filters or unavailable catalogue", http.StatusBadRequest)
		return
	}
	switch path {
	case "/fragments/channels":
		s.render(w, "channels", v)
	case "/fragments/status":
		s.render(w, "status", v)
	case "/fragments/jobs":
		s.render(w, "jobs", v)
	case "/fragments/playlist-options":
		s.render(w, "playlist-filter", v)
	default:
		s.render(w, "layout", v)
	}
}

func (s *Server) pageData(r *http.Request, v *page) error {
	path := r.URL.Path
	switch path {
	case "/catalogue":
		return s.videoPageData(r, v)
	case "/fragments/playlist-options":
		return s.playlistOptions(r, v)
	case "/channels", "/fragments/channels":
		q := r.URL.Query()
		filter := catalog.ChannelFilter{Query: q.Get("q"), Failure: q.Get("failure"), Sort: q.Get("sort"), Page: number(r, "page"), PerPage: number(r, "per_page")}
		result, err := s.App.Channels(r.Context(), filter, q.Get("policy"), q.Get("state"))
		if err != nil {
			return err
		}
		v.ChannelData = result.Items
		v.Pagination = paginate(q, "/channels", "Channels", result.Total)
	case "/playlists":
		result, err := s.App.Playlists(r.Context(), playlistFilter(r))
		if err != nil {
			return err
		}
		v.PlaylistRows = result.Items
		v.Pagination = paginate(r.URL.Query(), path, "Playlists", result.Total)
	case "/", "/jobs", "/fragments/jobs":
		result, err := s.App.Catalog.FilterJobs(r.Context(), jobFilter(r))
		if err != nil {
			return err
		}
		v.Status.Jobs = result.Items
		v.Pagination = paginate(r.URL.Query(), "/jobs", "Jobs", result.Total)
	case "/settings":
		data, err := yaml.Marshal(v.Status.Config.Config)
		if err != nil {
			return err
		}
		v.YAML = string(data)
	case "/artwork":
		return s.artworkPageData(r, v)
	}
	return nil
}

func (s *Server) videoPageData(r *http.Request, v *page) error {
	result, err := s.App.Videos(r.Context(), videoFilter(r))
	if err != nil {
		return err
	}
	v.Status.Videos = result.Items
	v.Matches = result.Total
	v.Total = v.Counts.Discovered
	v.Query = r.URL.Query().Get("q")
	v.Pagination = paginate(r.URL.Query(), "/catalogue", "Videos", result.Total)
	return s.playlistOptions(r, v)
}

func (s *Server) playlistOptions(r *http.Request, v *page) error {
	q := r.URL.Query()
	result, err := s.App.Playlists(r.Context(), catalog.PlaylistFilter{Query: q.Get("playlist_q"), Channel: q.Get("channel"), PerPage: 25})
	if err != nil {
		return err
	}
	v.PlaylistOptions = result.Items
	id := q.Get("playlist")
	if id == "" {
		return nil
	}
	for _, row := range result.Items {
		if row.ID == id {
			return nil
		}
	}
	v.PlaylistFallback = "Unavailable playlist (" + id + ")"
	selected, err := s.App.Playlists(r.Context(), catalog.PlaylistFilter{Query: id, PerPage: 25})
	if err != nil {
		return err
	}
	for _, row := range selected.Items {
		if row.ID == id {
			v.PlaylistFallback = row.Title
			break
		}
	}
	return nil
}

func (s *Server) artworkPageData(r *http.Request, v *page) error {
	q := r.URL.Query()
	channel, scope := q.Get("channel"), q.Get("scope")
	if scope == "" {
		scope = "show"
	}
	if channel == "" {
		return errors.New("select a channel")
	}
	preview, err := s.App.Artwork(r.Context(), channel, scope)
	if err != nil {
		return err
	}
	v.Artwork = preview
	seasons, err := s.App.ArtworkSeasons(r.Context(), catalog.SeasonFilter{Channel: channel, Query: q.Get("q"), Page: number(r, "page"), PerPage: number(r, "per_page")})
	if err != nil {
		return err
	}
	v.SeasonRows = seasons.Items
	v.SeasonPagination = paginate(q, "/artwork", "Seasons", seasons.Total)
	for _, season := range seasons.Items {
		if season.Scope == scope {
			v.CurrentSeasonOnPage = true
			break
		}
	}
	return nil
}
