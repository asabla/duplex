package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"duplex/internal/app"
	"duplex/internal/config"
)

type contentEditor struct {
	Name, Label, Mode, Limit string
}

type selectionEditor struct {
	Separate      bool
	Content       []contentEditor
	Keep, Exclude string
}

func editorFor(ch config.Channel) selectionEditor {
	e := selectionEditor{Separate: ch.Content != nil}
	for _, row := range []struct{ kind, name, label string }{{"upload", "uploads", "Uploads"}, {"short", "shorts", "Shorts"}, {"replay", "replays", "Completed streams"}} {
		policy := ch.ContentSelection(row.kind)
		e.Content = append(e.Content, contentEditor{row.name, row.label, policy.Mode, strconv.Itoa(max(1, policy.Limit))})
	}

	keep, exclude := []string{}, []string{}

	for id, mode := range ch.VideoOverrides {
		if mode == "keep" {
			keep = append(keep, id)
		} else {
			exclude = append(exclude, id)
		}
	}

	sort.Strings(keep)
	sort.Strings(exclude)
	e.Keep, e.Exclude = strings.Join(keep, "\n"), strings.Join(exclude, "\n")

	return e
}

func submittedEditor(r *http.Request) selectionEditor {
	e := selectionEditor{Separate: r.FormValue("separate") == "on", Keep: r.FormValue("keep"), Exclude: r.FormValue("exclude")}
	for _, row := range []struct{ name, label string }{{"uploads", "Uploads"}, {"shorts", "Shorts"}, {"replays", "Completed streams"}} {
		e.Content = append(e.Content, contentEditor{row.name, row.label, r.FormValue(row.name + "_mode"), r.FormValue(row.name + "_limit")})
	}

	return e
}

func editorChange(ch config.Channel, revision string, editor selectionEditor) (app.SelectionChange, error) {
	change := app.SelectionChange{ChannelID: ch.ID, Revision: revision, Selection: ch.Selection(), VideoOverrides: map[string]string{}}

	for _, row := range []struct{ ids, mode string }{{editor.Keep, "keep"}, {editor.Exclude, "exclude"}} {
		for _, id := range strings.Fields(row.ids) {
			if prior := change.VideoOverrides[id]; prior != "" && prior != row.mode {
				return change, errors.New("a video cannot be both kept and excluded")
			}

			change.VideoOverrides[id] = row.mode
		}
	}

	if editor.Separate {
		policies := map[string]config.Selection{}

		for _, row := range editor.Content {
			limit := 0

			if row.Mode == "latest" {
				var err error

				limit, err = strconv.Atoi(row.Limit)
				if err != nil || limit < 1 {
					return change, errors.New("each Latest selection needs a positive whole number")
				}
			}

			policies[row.Name] = config.Selection{Mode: row.Mode, Limit: limit}
		}

		change.Content = &config.ContentPolicy{Uploads: policies["uploads"], Shorts: policies["shorts"], Replays: policies["replays"]}
	}

	return change, nil
}

func (s *Server) selectionAPI(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "POST" || (r.URL.Path != "/api/v1/selection/preview" && r.URL.Path != "/api/v1/selection") {
		return false
	}

	var change app.SelectionChange
	if !decode(w, r, &change) {
		return true
	}

	if r.URL.Path == "/api/v1/selection" {
		s.result(w, r, s.App.SaveSelection(r.Context(), change), "Recording selection saved; refresh queued when enabled")
		return true
	}

	preview, err := s.App.PreviewSelection(r.Context(), change)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, config.ErrConflict) {
			status = http.StatusConflict
		}

		reply(w, status, map[string]string{"error": err.Error()})

		return true
	}

	reply(w, http.StatusOK, preview)

	return true
}

func (s *Server) selectionAction(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/actions/selection-preview", "/actions/video-selection-preview":
		s.previewSelectionPage(w, r)
	case "/actions/selection-save":
		var change app.SelectionChange

		decoder := json.NewDecoder(strings.NewReader(r.FormValue("change")))
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&change); err != nil {
			s.result(w, r, errors.New("invalid selection preview; preview again"), "")
			return true
		}

		if decoder.Decode(&struct{}{}) != io.EOF {
			s.result(w, r, errors.New("preview must contain one selection"), "")
			return true
		}

		err := s.App.SaveSelection(r.Context(), change)
		if err != nil {
			s.selectionFailurePage(w, r, change, err)
		} else {
			http.Redirect(w, r, channelLink("/selection", change.ChannelID, r.FormValue("back")), http.StatusSeeOther)
		}
	default:
		return false
	}

	return true
}

func (s *Server) previewSelectionPage(w http.ResponseWriter, r *http.Request) {
	status, counts, err := s.App.Summary(r.Context())
	if err != nil {
		http.Error(w, "catalogue unavailable", http.StatusServiceUnavailable)
		return
	}

	session, _ := s.Auth.Session(r)

	v := page{Page: "/selection", Tab: "settings", CSRF: session.CSRF, Status: status, Counts: counts, Revision: r.FormValue("revision"), ChannelSettings: map[string]config.Channel{}, BackURL: localPageURL(r.FormValue("back"), actionReturnURL(r))}
	for _, ch := range status.Config.Config.Channels {
		v.ChannelSettings[ch.ID] = ch
	}

	v.Channel = v.ChannelSettings[r.FormValue("id")]
	for _, info := range status.Channels {
		if info.ID == v.Channel.ID {
			v.ChannelInfo = info
		}
	}

	v.SelectionEditor = submittedEditor(r)
	if r.URL.Path == "/actions/video-selection-preview" {
		v.SelectionEditor, err = s.individualEditor(r.Context(), v.Channel, r.FormValue("video"), r.FormValue("override"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	change, err := editorChange(v.Channel, v.Revision, v.SelectionEditor)
	if err == nil {
		preview, failure := s.App.PreviewSelection(r.Context(), change)
		err = failure
		v.SelectionPreview = &preview
	}

	if err != nil {
		v.Error = err.Error()
		v.SelectionPreview = nil
	} else {
		encoded, failure := json.Marshal(change)
		if failure != nil {
			v.Error = "Cannot prepare selection preview"
			v.SelectionPreview = nil
		} else {
			v.SelectionJSON = string(encoded)
		}
	}

	s.render(w, "layout", v)
}

func (s *Server) individualEditor(ctx context.Context, ch config.Channel, id, mode string) (selectionEditor, error) {
	video, err := s.App.Video(ctx, id)
	if err != nil || video.ChannelID != ch.ID {
		return selectionEditor{}, errors.New("video does not belong to this channel")
	}

	if mode != "automatic" && mode != "keep" && mode != "exclude" {
		return selectionEditor{}, errors.New("invalid video selection")
	}

	if ch.VideoOverrides == nil {
		ch.VideoOverrides = map[string]string{}
	}

	delete(ch.VideoOverrides, id)

	if mode != "automatic" {
		ch.VideoOverrides[id] = mode
	}

	return editorFor(ch), nil
}

func (s *Server) selectionFailurePage(w http.ResponseWriter, r *http.Request, change app.SelectionChange, failure error) {
	status, _, err := s.App.Summary(r.Context())
	if err != nil {
		http.Error(w, "catalogue unavailable", http.StatusServiceUnavailable)
		return
	}

	session, _ := s.Auth.Session(r)

	ch := config.Channel{ID: change.ChannelID}
	for _, current := range status.Config.Config.Channels {
		if current.ID == change.ChannelID {
			ch = current
		}
	}

	ch.Indexing, ch.Content, ch.VideoOverrides = &change.Selection, change.Content, change.VideoOverrides
	v := page{Page: "/selection", Tab: "settings", CSRF: session.CSRF, Status: status, Revision: change.Revision, Channel: ch, SelectionEditor: editorFor(ch), Error: safeError(failure), BackURL: "/selection?channel=" + change.ChannelID}
	s.render(w, "layout", v)
}
