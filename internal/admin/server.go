package admin

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"duplex/internal/app"
)

//go:embed templates/*.html static/*
var content embed.FS

// Server serves the HTML and versioned JSON administration interfaces.
type Server struct {
	App       *app.App
	templates *template.Template
}

// New parses the embedded templates and binds the shared application services.
func New(a *app.App) (*Server, error) {
	t, e := template.New("duplex").Funcs(templateFunctions()).ParseFS(content, "templates/*.html")
	if e != nil {
		return nil, e
	}

	return &Server{App: a, templates: t}, nil
}

// Handler adds routing, CSRF protection, request limits, and browser security headers.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	assets, err := fs.Sub(content, "static")
	if err != nil {
		panic("embedded static assets are missing")
	}

	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(assets)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		writeText(w, "ok\n")
	})
	mux.HandleFunc("GET /mountz", func(w http.ResponseWriter, r *http.Request) {
		v, _, e := s.App.Summary(r.Context())
		if e != nil || !v.MountReady {
			http.Error(w, "mount not ready", http.StatusServiceUnavailable)
			return
		}

		writeText(w, "mount ready\n")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		v, _, e := s.App.Summary(r.Context())
		if e != nil || !v.Ready {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}

		writeText(w, "ready\n")
	})
	mux.Handle("/", s.protected(http.HandlerFunc(s.route)))

	return secureHandler(mux)
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/v1/") {
		s.api(w, r)
		return
	}

	if r.Method == http.MethodPost && s.legacyAction(w, r) {
		return
	}

	if s.actions(w, r) {
		return
	}

	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.readPage(w, r)
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	if s.readAPI(w, r) || s.mutateAPI(w, r) {
		return
	}

	if r.Method == http.MethodGet {
		s.compatibilityReadAPI(w, r)
		return
	}

	if s.compatibilityMutateAPI(w, r) || s.channelAPI(w, r) {
		return
	}

	reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "unsupported endpoint or method"})
}
