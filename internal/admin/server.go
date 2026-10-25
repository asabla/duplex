package admin

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"duplex/internal/app"
	"duplex/internal/catalog"
	"duplex/internal/config"
)

//go:embed templates/*.html static/*
var content embed.FS

// Server serves the authenticated HTML and versioned JSON administration interfaces.
type Server struct {
	App       *app.App
	Auth      *Auth
	templates *template.Template
}
type page struct {
	Page            string
	CSRF            string
	Error           string
	Notice          string
	Status          app.Status
	YAML            string
	Revision        string
	Query           string
	Total, Matches  int
	Pagination      pagination
	ChannelRows     []config.Channel
	ChannelData     []catalog.Channel
	ChannelSettings map[string]config.Channel
	PlaylistRows    []catalog.PlaylistRow
	Counts          catalog.Counts
	Filters         url.Values
	Artwork         app.ArtworkPreview
	Seasons         map[string]int
}

// New parses the embedded templates and binds the shared application services.
func New(a *app.App, auth *Auth) (*Server, error) {
	t, e := template.New("duplex").Funcs(template.FuncMap{"states": func(values ...string) []string { return values }, "heights": func(values ...int) []int { return values }, "sortlink": sortLink, "gib": func(n int64) string { return strconv.FormatFloat(float64(n)/(1<<30), 'f', 2, 64) }, "day": func(n int64) string {
		if n == 0 {
			return "Unknown"
		}
		return time.Unix(n, 0).UTC().Format("2006-01-02")
	}, "when": func(n int64) string {
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

// Handler adds routing, authentication, request limits, and browser security headers.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	assets, err := fs.Sub(content, "static")
	if err != nil {
		panic("embedded static assets are missing")
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(assets)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		writeText(w, "ok\n")
	})
	mux.HandleFunc("GET /mountz", func(w http.ResponseWriter, r *http.Request) {
		v, _, e := s.App.Summary(r.Context())
		if e != nil || !v.MountReady {
			http.Error(w, "mount not ready", 503)
			return
		}
		writeText(w, "mount ready\n")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		v, _, e := s.App.Summary(r.Context())
		if e != nil || !v.Ready {
			http.Error(w, "not ready", 503)
			return
		}
		writeText(w, "ready\n")
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
		limit := int64(1 << 20)
		if r.URL.Path == "/actions/artwork" || r.URL.Path == "/api/v1/artwork" {
			limit = 10<<20 + 64<<10
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
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
	var rendered bytes.Buffer
	if err := s.templates.ExecuteTemplate(&rendered, name, v); err != nil {
		slog.Error("page rendering failed")
		http.Error(w, "page unavailable", http.StatusInternalServerError)
		return
	}
	if _, err := rendered.WriteTo(w); err != nil {
		slog.Error("page response write failed")
	}
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("JSON response write failed")
	}
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
		mode := r.FormValue("mode")
		if mode == "" {
			mode = "latest"
		}
		if mode != "latest" {
			limit = 0
		}
		e := s.App.Follow(r.Context(), r.FormValue("url"), config.Selection{Mode: mode, Limit: limit}, r.FormValue("include_playlists") == "on", r.FormValue("revision"))
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
	if s.actions(w, r) {
		return
	}
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.readPage(w, r)
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	if s.readAPI(w, r) || s.mutateAPI(w, r) {
		return
	}
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
		case "/api/v1/diagnostics":
			v, _, e := s.App.Summary(r.Context())
			if e != nil {
				reply(w, 503, map[string]string{"error": "diagnostics unavailable"})
				return
			}
			reply(w, 200, v)
		case "/api/v1/status":
			v, _, e := s.App.Summary(r.Context())
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

func (p page) ChannelName(id string) string {
	for _, ch := range p.Status.Channels {
		if ch.ID == id && ch.Title != "" {
			return ch.Title
		}
	}
	return id
}

func writeText(w http.ResponseWriter, text string) {
	if _, err := io.WriteString(w, text); err != nil {
		slog.Error("text response write failed")
	}
}
