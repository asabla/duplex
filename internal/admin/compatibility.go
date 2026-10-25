package admin

import (
	"net/http"
	"strconv"
	"strings"

	"duplex/internal/config"
)

func (s *Server) legacyAction(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if p == "/actions/follow" && r.Method == "POST" {
		s.followAction(w, r)
		return true
	}

	if p == "/actions/sync" && r.Method == "POST" {
		e := s.App.Queue(r.FormValue("id"))
		s.result(w, r, e, "Channel refresh queued.")
		return true
	}
	if p == "/actions/remove" && r.Method == "POST" {
		e := s.App.RemoveChannel(r.FormValue("id"), r.FormValue("revision"))
		s.result(w, r, e, "Channel removed from the library. Its identity records are retained.")
		return true
	}
	if p == "/actions/config" && r.Method == "POST" {
		c, e := config.Decode([]byte(r.FormValue("yaml")))
		if e == nil {
			e = s.App.Config.Save(c, r.FormValue("revision"))
		}
		s.result(w, r, e, "Settings saved. Reload this page before making another edit.")
		return true
	}

	return false
}

func (s *Server) followAction(w http.ResponseWriter, r *http.Request) {
	mode := r.FormValue("mode")
	if mode == "" {
		mode = "latest"
	}
	limit := 0
	var err error
	if mode == "latest" {
		limit, err = strconv.Atoi(r.FormValue("limit"))
	}
	if err == nil {
		err = s.App.Follow(r.Context(), r.FormValue("url"), config.Selection{Mode: mode, Limit: limit}, r.FormValue("include_playlists") == "on", r.FormValue("revision"))
	}
	s.result(w, r, err, "Channel followed. A refresh is queued.")
}

func (s *Server) compatibilityReadAPI(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v1/config":
		reply(w, 200, s.App.Config.Snapshot())
	case "/api/v1/channels":
		v, e := s.App.Catalog.Channels(r.Context())
		if e != nil {
			reply(w, 503, map[string]string{"error": "catalogue unavailable"})
			return
		}
		reply(w, 200, map[string]any{"settings": s.App.Config.Snapshot().Config.Channels, "channels": v})
	case "/api/v1/jobs":
		v, e := s.App.Catalog.Jobs(r.Context())
		if e != nil {
			reply(w, 503, map[string]string{"error": "jobs unavailable"})
			return
		}
		reply(w, 200, v)
	case "/api/v1/diagnostics":
		v, _, e := s.App.Summary(r.Context())
		if e != nil {
			reply(w, 503, map[string]string{"error": "diagnostics unavailable"})
			return
		}
		reply(w, 200, v)
	case "/api/v1/status":
		v, e := s.App.Status(r.Context())
		if e != nil {
			reply(w, 503, map[string]string{"error": "status unavailable"})
			return
		}
		reply(w, 200, v)
	default:
		reply(w, 404, map[string]string{"error": "endpoint not found"})
	}
}

func (s *Server) compatibilityMutateAPI(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if p == "/api/v1/config" && r.Method == "PUT" {
		var v struct {
			Revision string        `json:"revision"`
			Config   config.Config `json:"config"`
		}
		if !decode(w, r, &v) {
			return true
		}
		s.result(w, r, s.App.Config.Save(v.Config, v.Revision), "Settings saved")
		return true
	}
	if p == "/api/v1/channels" && r.Method == "POST" {
		var v struct {
			URL      string `json:"url"`
			Limit    int    `json:"limit"`
			Revision string `json:"revision"`
		}
		if !decode(w, r, &v) {
			return true
		}
		s.result(w, r, s.App.AddChannel(r.Context(), v.URL, v.Limit, v.Revision), "Channel followed")
		return true
	}

	return false
}

func (s *Server) channelAPI(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/channels/") {
		return false
	}
	{
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/channels/"), "/")
		if len(parts) == 2 && parts[1] == "sync" && r.Method == "POST" {
			s.result(w, r, s.App.Queue(parts[0]), "Refresh queued")
			return true
		}
		if len(parts) == 1 && r.Method == "DELETE" {
			var v struct {
				Revision string `json:"revision"`
			}
			if !decode(w, r, &v) {
				return true
			}
			s.result(w, r, s.App.RemoveChannel(parts[0], v.Revision), "Channel removed")
			return true
		}
	}
	return false
}
