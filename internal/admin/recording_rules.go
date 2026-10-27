package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"duplex/internal/app"
	"duplex/internal/catalog"
)

func (s *Server) editRecordingRule(r *http.Request, v *page, change app.SelectionChange) (app.SelectionChange, error) {
	mode, raw, _ := strings.Cut(r.FormValue("recording_action"), ":")

	id, err := recordingID(raw)
	if err != nil {
		return change, fieldFailure{"recording-query", err.Error()}
	}

	video, err := s.App.Video(r.Context(), id)
	if err != nil || video.ChannelID != v.Channel.ID {
		return change, fieldFailure{"recording-query", "Choose a recording discovered for this channel."}
	}

	if mode != "keep" && mode != "exclude" && mode != "automatic" {
		return change, errors.New("invalid recording rule")
	}

	delete(change.VideoOverrides, id)

	if mode != "automatic" {
		change.VideoOverrides[id] = mode
	}

	ch := v.Channel
	ch.VideoOverrides = change.VideoOverrides
	editor := editorFor(ch)
	v.SelectionEditor.Keep, v.SelectionEditor.Exclude = editor.Keep, editor.Exclude
	v.Notice = "Draft rule updated. Preview the selection, then save to apply it."

	return change, nil
}

func (s *Server) recordingSearchData(r *http.Request, v *page) error {
	v.RecordingSearchOpen = r.URL.Path == "/actions/selection-search" || v.RecordingQuery != "" || v.ErrorFieldID == "recording-query"

	query := strings.TrimSpace(v.RecordingQuery)
	if len(query) > 200 {
		return errors.New("recording search is too long")
	}

	if id, err := recordingID(query); err == nil {
		query = id
	}

	ch := v.Channel
	if change, err := editorChange(ch, v.Revision, v.SelectionEditor); err == nil {
		ch.VideoOverrides = change.VideoOverrides
	}

	number := formPage(r, "recording_page")
	result, err := s.App.Catalog.SearchRecordings(r.Context(), ch, query, number)
	v.RecordingSearch = result

	return err
}

func (p page) RecordingRule(v catalog.Video) string {
	return p.ChannelSettings[v.ChannelID].VideoOverrides[v.ID]
}

func (p page) RecordingReason(v catalog.Video) string {
	switch p.RecordingRule(v) {
	case "keep":
		return "Keep rule for channel recordings"
	case "exclude":
		if v.Exposed {
			return "Excluded from channel recordings; selected through a playlist"
		}

		return "Explicitly excluded from channel recordings"
	default:
		if !p.ChannelSettings[v.ChannelID].Enabled {
			return "Channel is paused or no longer followed"
		}

		if !v.Exposed {
			return "Outside the automatic recording and playlist selections"
		}

		return "Automatic selection"
	}
}

func contentLabel(kind string) string {
	labels := map[string]string{"upload": "Upload", "short": "Short", "replay": "Completed stream"}
	if label := labels[kind]; label != "" {
		return label
	}

	return "Type not confirmed"
}

func formPage(r *http.Request, name string) int {
	n, err := strconv.Atoi(r.FormValue(name))
	if err != nil {
		return 1
	}

	return max(1, n)
}

func (s *Server) selectionPageData(r *http.Request, v *page) error {
	if v.Channel.ID == "" {
		return errors.New("channel is not followed")
	}

	v.Tab = "settings"

	v.SelectionEditor = editorFor(v.Channel)
	if r.URL.Query().Get("saved") == "1" {
		v.Notice = "Selection saved. The channel is paused; discovery was not queued."
		if v.Channel.Enabled {
			v.Notice = "Selection saved. Channel discovery was queued."
		}
	}

	return s.recordingSearchData(r, v)
}
