package admin

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"duplex/internal/config"
)

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
		s.render(w, "notice", page{Error: safeError(e), ErrorFieldID: errorField(r, e), ReturnURL: actionReturnURL(r)})
		return
	}
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, actionReturnURL(r), http.StatusSeeOther) // #nosec G710 -- actionReturnURL permits only enumerated same-host administration paths.
		return
	}
	w.Header().Set("HX-Trigger", "channelsChanged")
	s.render(w, "notice", page{Notice: message, ReturnURL: actionReturnURL(r)})
	s.savedForm(w, r)
}

func writeText(w http.ResponseWriter, text string) {
	if _, err := io.WriteString(w, text); err != nil {
		slog.Error("text response write failed")
	}
}
