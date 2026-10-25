package admin

import (
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"duplex/internal/app"
	"duplex/internal/config"
	"gopkg.in/yaml.v3"
)

//go:embed templates/*.html static/*
var content embed.FS

type Server struct {
	App       *app.App
	Auth      *Auth
	templates *template.Template
}
type page struct {
	Page           string
	CSRF           string
	Error          string
	Notice         string
	Status         app.Status
	YAML           string
	Revision       string
	Query          string
	Total, Matches int
	Pagination     pagination
	ChannelRows    []config.Channel
}

func New(a *app.App, auth *Auth) (*Server, error) {
	t, e := template.New("duplex").Funcs(template.FuncMap{"gib": func(n int64) string { return strconv.FormatFloat(float64(n)/(1<<30), 'f', 2, 64) }, "when": func(n int64) string {
		if n == 0 {
			return "Never"
		}
		return time.Unix(n, 0).UTC().Format("2006-01-02 15:04 MST")
	}, "height": func(n int) string {
		if n == 0 {
			return "Unavailable"
		}
		return strconv.Itoa(n) + "p"
	}, "age": func(n int64) string {
		if n == 0 {
			return "Never"
		}
		elapsed := time.Since(time.Unix(n, 0))
		if elapsed < time.Minute {
			return "Just refreshed"
		}
		if elapsed < time.Hour {
			return strconv.Itoa(int(elapsed.Minutes())) + " minutes ago"
		}
		if elapsed < 24*time.Hour {
			return strconv.Itoa(int(elapsed.Hours())) + " hours ago"
		}
		return strconv.Itoa(int(elapsed.Hours()/24)) + " days ago"
	}, "inc": func(n int) int { return n + 1 }}).ParseFS(content, "templates/*.html")
	if e != nil {
		return nil, e
	}
	return &Server{App: a, Auth: auth, templates: t}, nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	assets, _ := fs.Sub(content, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(assets)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /mountz", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.App.Status(r.Context())
		if e != nil || !v.MountReady {
			http.Error(w, "mount not ready", 503)
			return
		}
		w.Write([]byte("mount ready\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.App.Status(r.Context())
		if e != nil || !v.Ready {
			http.Error(w, "not ready", 503)
			return
		}
		w.Write([]byte("ready\n"))
	})
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("GET /api/v1/session", s.session)
	mux.HandleFunc("POST /api/v1/login", s.login)
	mux.Handle("/", s.protected(http.HandlerFunc(s.route)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) protected(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, e := s.Auth.Authorize(r)
		if e != nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				code := 403
				if _, ok := s.Auth.Session(r); !ok {
					code = 401
				}
				reply(w, code, map[string]string{"error": e.Error()})
			} else {
				if r.Header.Get("HX-Request") == "true" {
					w.Header().Set("HX-Redirect", "/login")
				}
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	csrf := s.Auth.LoginCSRF(w, r)
	s.render(w, "login", page{CSRF: csrf})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Username string `json:"username"`
		Password string `json:"password"`
		CSRF     string `json:"csrf"`
	}
	api := strings.HasPrefix(r.URL.Path, "/api/")
	if api {
		if !decode(w, r, &v) {
			return
		}
		if v.CSRF == "" {
			v.CSRF = r.Header.Get("X-CSRF-Token")
		}
	} else {
		v.Username, v.Password, v.CSRF = r.FormValue("username"), r.FormValue("password"), r.FormValue("csrf")
	}
	if len(v.Password) > 1024 {
		http.Error(w, "invalid credentials", 400)
		return
	}
	if e := s.Auth.Login(w, r, v.Username, v.Password, v.CSRF); e != nil {
		if api {
			reply(w, 401, map[string]string{"error": e.Error()})
		} else {
			w.WriteHeader(401)
			s.render(w, "login", page{CSRF: s.Auth.LoginCSRF(w, r), Error: e.Error()})
		}
		return
	}
	if api {
		reply(w, 200, map[string]bool{"authenticated": true})
	} else {
		http.Redirect(w, r, "/", 303)
	}
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.Auth.Session(r); ok {
		reply(w, 200, map[string]any{"authenticated": true, "csrf": v.CSRF, "expires": v.Expires})
	} else {
		reply(w, 200, map[string]any{"authenticated": false, "csrf": s.Auth.LoginCSRF(w, r)})
	}
}
func (s *Server) render(w http.ResponseWriter, name string, v page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(w, name, v)
}
func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		reply(w, 400, map[string]string{"error": "invalid request JSON"})
		return false
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		reply(w, 400, map[string]string{"error": "request must contain one JSON document"})
		return false
	}
	return true
}
func safeError(e error) string {
	var p *os.PathError
	if errors.As(e, &p) {
		return "Cannot access a required local file. Check the service directories and file permissions."
	}
	return e.Error()
}
func (s *Server) result(w http.ResponseWriter, r *http.Request, e error, message string) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if e != nil {
			code := 400
			if errors.Is(e, config.ErrConflict) {
				code = 409
			}
			reply(w, code, map[string]string{"error": safeError(e)})
			return
		}
		reply(w, 200, map[string]string{"message": message})
		return
	}
	if e != nil {
		w.WriteHeader(400)
		s.render(w, "notice", page{Error: safeError(e)})
		return
	}
	w.Header().Set("HX-Trigger", "channelsChanged")
	s.render(w, "notice", page{Notice: message})
}
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/logout" && r.Method == "POST" {
		s.Auth.Logout(w, r)
		http.Redirect(w, r, "/login", 303)
		return
	}
	if strings.HasPrefix(p, "/api/v1/") {
		s.api(w, r)
		return
	}
	if p == "/actions/follow" && r.Method == "POST" {
		limit, _ := strconv.Atoi(r.FormValue("limit"))
		e := s.App.AddChannel(r.Context(), r.FormValue("url"), limit, r.FormValue("revision"))
		s.result(w, r, e, "Channel followed. A refresh is queued.")
		return
	}
	if p == "/actions/sync" && r.Method == "POST" {
		e := s.App.Queue(r.FormValue("id"))
		s.result(w, r, e, "Channel refresh queued.")
		return
	}
	if p == "/actions/remove" && r.Method == "POST" {
		e := s.App.RemoveChannel(r.FormValue("id"), r.FormValue("revision"))
		s.result(w, r, e, "Channel removed from the library. Its identity records are retained.")
		return
	}
	if p == "/actions/config" && r.Method == "POST" {
		c, e := config.Decode([]byte(r.FormValue("yaml")))
		if e == nil {
			e = s.App.Config.Save(c, r.FormValue("revision"))
		}
		s.result(w, r, e, "Settings saved. Reload this page before making another edit.")
		return
	}
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if p != "/" && p != "/channels" && p != "/catalogue" && p != "/settings" && p != "/diagnostics" && p != "/fragments/channels" && p != "/fragments/status" {
		http.NotFound(w, r)
		return
	}
	status, e := s.App.Status(r.Context())
	if e != nil {
		http.Error(w, "catalogue unavailable", 503)
		return
	}
	session, _ := s.Auth.Session(r)
	b, _ := yaml.Marshal(status.Config.Config)
	v := page{Page: p, CSRF: session.CSRF, Status: status, YAML: string(b), Revision: status.Config.Revision}
	if p == "/catalogue" {
		v.Query = strings.TrimSpace(r.URL.Query().Get("q"))
		if len(v.Query) > 200 {
			http.Error(w, "search is too long", 400)
			return
		}
		v.Total = len(status.Videos)
		needle := strings.ToLower(v.Query)
		rows := status.Videos[:0:0]
		for _, video := range status.Videos {
			if needle == "" || strings.Contains(strings.ToLower(video.Title+" "+video.ID+" "+video.ChannelID), needle) {
				rows = append(rows, video)
			}
		}
		v.Matches = len(rows)
		v.Pagination = paginate(r.URL.Query(), "/catalogue", "Videos", len(rows))
		v.Status.Videos = rows[v.Pagination.start():v.Pagination.end()]
	} else if p == "/channels" || p == "/fragments/channels" {
		v.Pagination = paginate(r.URL.Query(), "/channels", "Channels", len(status.Config.Config.Channels))
		v.ChannelRows = status.Config.Config.Channels[v.Pagination.start():v.Pagination.end()]
	} else if p == "/" || p == "/diagnostics" {
		requested := paginate(r.URL.Query(), p, "Refresh history", 0)
		// Query the complete durable history, not Status's latest-100 snapshot.
		jobs, total, err := s.App.Catalog.JobsPage(r.Context(), requested.requested, requested.Size)
		if err != nil {
			http.Error(w, "refresh history unavailable", 503)
			return
		}
		v.Status.Jobs = jobs
		v.Pagination = paginate(r.URL.Query(), p, "Refresh history", total)
	}

	if p == "/fragments/channels" {
		s.render(w, "channels", v)
	} else if p == "/fragments/status" {
		s.render(w, "status", v)
	} else {
		s.render(w, "layout", v)
	}
}
func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if r.Method == "GET" {
		switch p {
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
		case "/api/v1/status", "/api/v1/diagnostics":
			v, e := s.App.Status(r.Context())
			if e != nil {
				reply(w, 503, map[string]string{"error": "status unavailable"})
				return
			}
			reply(w, 200, v)
		default:
			reply(w, 404, map[string]string{"error": "endpoint not found"})
		}
		return
	}
	if p == "/api/v1/config" && r.Method == "PUT" {
		var v struct {
			Revision string        `json:"revision"`
			Config   config.Config `json:"config"`
		}
		if !decode(w, r, &v) {
			return
		}
		s.result(w, r, s.App.Config.Save(v.Config, v.Revision), "Settings saved")
		return
	}
	if p == "/api/v1/channels" && r.Method == "POST" {
		var v struct {
			URL      string `json:"url"`
			Limit    int    `json:"limit"`
			Revision string `json:"revision"`
		}
		if !decode(w, r, &v) {
			return
		}
		s.result(w, r, s.App.AddChannel(r.Context(), v.URL, v.Limit, v.Revision), "Channel followed")
		return
	}
	if strings.HasPrefix(p, "/api/v1/channels/") {
		parts := strings.Split(strings.TrimPrefix(p, "/api/v1/channels/"), "/")
		if len(parts) == 2 && parts[1] == "sync" && r.Method == "POST" {
			s.result(w, r, s.App.Queue(parts[0]), "Refresh queued")
			return
		}
		if len(parts) == 1 && r.Method == "DELETE" {
			var v struct {
				Revision string `json:"revision"`
			}
			if !decode(w, r, &v) {
				return
			}
			s.result(w, r, s.App.RemoveChannel(parts[0], v.Revision), "Channel removed")
			return
		}
	}
	reply(w, 405, map[string]string{"error": "unsupported endpoint or method"})
}
