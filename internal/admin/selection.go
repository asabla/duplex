package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
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

func (e selectionEditor) OverrideCount() int {
	return len(strings.Fields(e.Keep)) + len(strings.Fields(e.Exclude))
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

	var err error

	change.VideoOverrides, err = editorOverrides(editor)
	if err != nil {
		return change, err
	}

	if editor.Separate {
		policies := map[string]config.Selection{}

		for _, row := range editor.Content {
			if row.Mode != "latest" && row.Mode != "all" && row.Mode != "skip" {
				return change, fieldFailure{"content-" + row.Name + "-mode", "Choose Latest recordings, All recordings, or Skip for " + row.Label + "."}
			}

			limit := 0

			if row.Mode == "latest" {
				var err error

				limit, err = strconv.Atoi(row.Limit)
				if err != nil || limit < 1 {
					return change, fieldFailure{"content-" + row.Name + "-limit", "Enter a positive whole number for " + row.Label + "."}
				}
			}

			policies[row.Name] = config.Selection{Mode: row.Mode, Limit: limit}
		}

		change.Content = &config.ContentPolicy{Uploads: policies["uploads"], Shorts: policies["shorts"], Replays: policies["replays"]}
	}

	return change, nil
}

func editorOverrides(editor selectionEditor) (map[string]string, error) {
	overrides := map[string]string{}

	for _, row := range []struct{ ids, mode string }{{editor.Keep, "keep"}, {editor.Exclude, "exclude"}} {
		for _, raw := range strings.Fields(row.ids) {
			id, err := recordingID(raw)
			if err != nil {
				return nil, fieldFailure{"selection-" + row.mode, err.Error()}
			}

			if prior := overrides[id]; prior != "" && prior != row.mode {
				return nil, fieldFailure{"selection-" + row.mode, "A recording cannot be both kept and excluded. Remove it from one list."}
			}

			overrides[id] = row.mode
		}
	}

	return overrides, nil
}

// recordingID accepts IDs and ordinary watch, Shorts, or shortened URLs without
// making a source request. Credentials, other hosts, and ambiguous paths fail.
func recordingID(raw string) (string, error) {
	id := raw
	if u, err := url.Parse(raw); err == nil && u.IsAbs() && u.User == nil && u.Scheme == "https" {
		switch u.Host {
		case "youtu.be":
			id = strings.TrimPrefix(u.Path, "/")
		case "www.youtube.com", "youtube.com", "m.youtube.com":
			if u.Path == "/watch" {
				id = u.Query().Get("v")
			} else if strings.HasPrefix(u.Path, "/shorts/") || strings.HasPrefix(u.Path, "/live/") {
				id = strings.TrimPrefix(strings.TrimPrefix(u.Path, "/shorts/"), "/live/")
			}
		}
	}

	if len(id) != 11 || strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return "", fieldFailure{"", "Enter an eleven-character video ID or a YouTube recording URL."}
	}

	return id, nil
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

	view := r.URL.Query().Get("view")
	if view == "" {
		view = "selected"
	}

	preview, err := s.App.PreviewSelectionPage(r.Context(), change, view, number(r, "page"))
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
