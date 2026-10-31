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
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			ensureCSRF(w, r)
		} else if err := checkCSRF(r); err != nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				reply(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			} else {
				w.WriteHeader(http.StatusForbidden)
				s.render(w, "notice", page{Error: err.Error(), ReturnURL: actionReturnURL(r)})
			}

			return
		}

		next.ServeHTTP(w, r)
	})
}
