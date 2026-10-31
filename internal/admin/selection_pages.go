package admin

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"reflect"
	"strings"

	"duplex/internal/app"
	"duplex/internal/config"
)

func (s *Server) selectionAction(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/actions/selection-preview", "/actions/video-selection-preview", "/actions/selection-search":
		s.previewSelectionPage(w, r)
	case "/actions/selection-save":
		s.saveSelectionPage(w, r)
	default:
		return false
	}

	return true
}

func (s *Server) selectionDraft(r *http.Request) (page, error) {
	status, counts, err := s.App.Summary(r.Context())
	if err != nil {
		return page{}, err
	}

	v := page{Page: "/selection", Tab: "settings", CSRF: csrfToken(r), Status: status, Counts: counts, Revision: r.FormValue("revision"), ChannelSettings: map[string]config.Channel{}, BackURL: localPageURL(r.FormValue("back"), actionReturnURL(r)), RecordingQuery: r.FormValue("recording_q")}
	for _, ch := range status.Config.Config.Channels {
		v.ChannelSettings[ch.ID] = ch
	}

	v.Channel = v.ChannelSettings[r.FormValue("id")]
	if v.Channel.ID == "" {
		return v, errors.New("channel is not followed")
	}

	for _, info := range status.Channels {
		if info.ID == v.Channel.ID {
			v.ChannelInfo = info
		}
	}

	v.SelectionEditor = submittedEditor(r)

	return v, nil
}

func (s *Server) previewSelectionPage(w http.ResponseWriter, r *http.Request) {
	v, err := s.selectionDraft(r)
	if err != nil {
		http.Error(w, "cannot read recording selection", http.StatusBadRequest)
		return
	}

	if r.URL.Path == "/actions/video-selection-preview" {
		v.SelectionEditor, err = s.individualEditor(r.Context(), v.Channel, r.FormValue("video"), r.FormValue("override"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	change, err := editorChange(v.Channel, v.Revision, v.SelectionEditor)
	if err == nil && r.FormValue("recording_action") != "" {
		change, err = s.editRecordingRule(r, &v, change)
	}

	if err == nil && r.URL.Path != "/actions/selection-search" {
		err = s.reviewSelection(r, &v, change)
	}

	s.renderSelectionDraft(w, r, v, err)
}

func (s *Server) reviewSelection(r *http.Request, v *page, change app.SelectionChange) error {
	view := r.FormValue("view")
	if view == "" {
		view = "selected"
	}

	pageNumber := formPage(r, "preview_page")
	if review := r.FormValue("review"); review != "" {
		view, pageNumber = review, 1
	}

	preview, err := s.App.PreviewSelectionPage(r.Context(), change, view, pageNumber)
	if err != nil {
		return err
	}

	encoded, err := json.Marshal(change)
	if err == nil {
		v.SelectionPreview, v.SelectionJSON = &preview, string(encoded)
	}

	return err
}

func (s *Server) renderSelectionDraft(w http.ResponseWriter, r *http.Request, v page, failure error) {
	if failure != nil {
		v.Error = safeError(failure)
		v.SelectionConflict = errors.Is(failure, config.ErrConflict)
		v.SelectionPreview = nil

		var field fieldFailure
		if errors.As(failure, &field) {
			v.ErrorFieldID = field.field
		}
	}

	if err := s.recordingSearchData(r, &v); err != nil && v.Error == "" {
		v.Error = "Cannot search the recording catalogue. Try again."
		v.ErrorFieldID = "recording-query"
		v.SelectionPreview = nil
	}

	s.render(w, "layout", v)
}

func (s *Server) saveSelectionPage(w http.ResponseWriter, r *http.Request) {
	var reviewed app.SelectionChange

	decoder := json.NewDecoder(strings.NewReader(r.FormValue("change")))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&reviewed); err != nil {
		s.result(w, r, errors.New("invalid selection preview; preview again"), "")
		return
	}

	if decoder.Decode(&struct{}{}) != io.EOF {
		s.result(w, r, errors.New("preview must contain one selection"), "")
		return
	}

	v, err := s.reviewedDraft(r, reviewed)
	if err == nil {
		err = s.App.SaveSelection(r.Context(), reviewed)
	}

	if err != nil {
		s.renderSelectionDraft(w, r, v, err)
		return
	}

	target := channelLink("/selection", reviewed.ChannelID, r.FormValue("back")) + "&saved=1"
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// Compare submitted visible fields with the reviewed draft on the server. This
// also protects plain HTML users when JavaScript cannot invalidate a preview.
func (s *Server) reviewedDraft(r *http.Request, reviewed app.SelectionChange) (page, error) {
	if r.FormValue("editor") != "1" {
		r.Form.Set("id", reviewed.ChannelID)
		r.Form.Set("revision", reviewed.Revision)
	}

	v, err := s.selectionDraft(r)
	if err != nil {
		return v, err
	}

	if r.FormValue("editor") != "1" {
		ch := v.Channel
		ch.Indexing, ch.Content, ch.VideoOverrides = &reviewed.Selection, reviewed.Content, reviewed.VideoOverrides
		v.SelectionEditor = editorFor(ch)

		return v, nil
	}

	current, err := editorChange(v.Channel, v.Revision, v.SelectionEditor)
	if err != nil {
		return v, err
	}

	if v.Revision != v.Status.Config.Revision {
		return v, config.ErrConflict
	}

	if reviewed.ChannelID != current.ChannelID || reviewed.Revision != current.Revision || reviewed.Selection != current.Selection || !reflect.DeepEqual(reviewed.Content, current.Content) || !maps.Equal(reviewed.VideoOverrides, current.VideoOverrides) {
		return v, fieldFailure{"", "The rules changed after preview. Preview the current rules before saving."}
	}

	return v, nil
}
