package admin

import (
	"net/http"
	"strings"
)

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
		http.Error(w, "invalid credentials", http.StatusBadRequest)
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
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.Auth.Session(r); ok {
		reply(w, 200, map[string]any{"authenticated": true, "csrf": v.CSRF, "expires": v.Expires})
	} else {
		reply(w, 200, map[string]any{"authenticated": false, "csrf": s.Auth.LoginCSRF(w, r)})
	}
}
