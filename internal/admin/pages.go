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
	return catalog.PlaylistFilter{Query: q.Get("q"), Channel: q.Get("channel"), Mode: q.Get("policy"), Failure: q.Get("failure"), Sort: q.Get("sort"), Page: number(r, "page"), PerPage: number(r, "per_page")}
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
	case "/", "/channels", "/playlists", "/catalogue", "/jobs", "/settings", "/diagnostics", "/artwork", "/fragments/channels", "/fragments/status", "/fragments/jobs":
	default:
		http.NotFound(w, r)
		return
	}
	if len(r.URL.RawQuery) > 8192 || len(r.URL.Query().Get("q")) > 200 {
		http.Error(w, "filter is too long", 400)
		return
	}
	status, counts, err := s.App.Summary(r.Context())
	if err != nil {
		http.Error(w, "catalogue unavailable", 503)
		return
	}
	session, _ := s.Auth.Session(r)
	v := page{Page: path, CSRF: session.CSRF, Status: status, Revision: status.Config.Revision, Counts: counts, Filters: r.URL.Query(), ChannelSettings: map[string]config.Channel{}}
	for _, ch := range status.Config.Config.Channels {
		v.ChannelSettings[ch.ID] = ch
	}
	if err = s.pageData(r, &v); err != nil {
		http.Error(w, "cannot read this page: invalid filters or unavailable catalogue", 400)
		return
	}
	switch path {
	case "/fragments/channels":
		s.render(w, "channels", v)
	case "/fragments/status":
		s.render(w, "status", v)
	case "/fragments/jobs":
		s.render(w, "job-progress", v)
	default:
		s.render(w, "layout", v)
	}
}

func (s *Server) pageData(r *http.Request, v *page) error {
	path := r.URL.Path
	switch path {
	case "/catalogue":
		result, err := s.App.Videos(r.Context(), videoFilter(r))
		if err != nil {
			return err
		}
		v.Status.Videos = result.Items
		v.Matches = result.Total
		v.Total = v.Counts.Discovered
		v.Query = r.URL.Query().Get("q")
		v.Pagination = paginate(r.URL.Query(), path, "Videos", result.Total)
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
	case "/", "/jobs":
		result, err := s.App.Catalog.FilterJobs(r.Context(), jobFilter(r))
		if err != nil {
			return err
		}
		v.Status.Jobs = result.Items
		v.Pagination = paginate(r.URL.Query(), path, "Jobs", result.Total)
	case "/settings":
		data, err := yaml.Marshal(v.Status.Config.Config)
		if err != nil {
			return err
		}
		v.YAML = string(data)
	case "/artwork":
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
		v.Seasons, err = s.App.Catalog.Seasons(r.Context(), channel)
		return err
	}
	return nil
}
