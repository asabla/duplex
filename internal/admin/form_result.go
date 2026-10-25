package admin

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"duplex/internal/app"
	"duplex/internal/config"
)

type fieldFailure struct{ field, message string }

func (e fieldFailure) Error() string { return e.message }

func wholeNumber(r *http.Request, name string, minValue, maxValue int) (int, error) {
	n, err := strconv.Atoi(r.FormValue(name))
	if err != nil || n < minValue || n > maxValue {
		if name == "limit" {
			return 0, fieldFailure{name, "Enter a positive whole number of videos."}
		}

		return 0, fieldFailure{name, fmt.Sprintf("Enter a whole number from %d to %d.", minValue, maxValue)}
	}

	return n, nil
}

func errorField(r *http.Request, err error) string {
	var failure fieldFailure

	form := r.FormValue("_form")
	if !errors.As(err, &failure) || form == "" || len(form) > 80 || strings.Trim(form, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		return ""
	}

	return form + "-" + failure.field
}

func (s *Server) savedForm(w http.ResponseWriter, r *http.Request) {
	if s.App == nil || s.Auth == nil {
		return
	}

	session, ok := s.Auth.Session(r)
	if !ok {
		return
	}

	snap := s.App.Config.Snapshot()

	v := page{CSRF: session.CSRF, Status: app.Status{Config: snap}, Revision: snap.Revision, ChannelSettings: map[string]config.Channel{}, OOB: true}
	for _, ch := range snap.Config.Channels {
		v.ChannelSettings[ch.ID] = ch
	}

	switch r.URL.Path {
	case "/actions/policy":
		form := v.PolicyForm(r.FormValue("id"), r.FormValue("playlist"))
		if r.FormValue("_form") != form.ID {
			return
		}

		form.OOB = true
		s.render(w, "policy-form", form)

		if form.PlaylistID != "" {
			s.render(w, "policy-summary", form)
		}
	case "/actions/settings":
		if r.FormValue("_form") == "settings-form" {
			s.render(w, "settings-form", v)
		}
	case "/actions/config":
		data, err := yaml.Marshal(snap.Config)
		if err != nil {
			slog.Error("saved configuration form could not be rendered")
			return
		}

		v.YAML = string(data)
		if r.FormValue("_form") == "yaml-form" {
			s.render(w, "yaml-form", v)
		}
	}
}
