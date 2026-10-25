package admin

import (
	"net/http"
	"strings"
)

// secureHandler applies response headers and request limits before routing.
func secureHandler(next http.Handler) http.Handler {
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
		next.ServeHTTP(w, r)
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
