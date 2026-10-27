package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"duplex/internal/catalog"
	"duplex/internal/config"
)

// SelectionChange replaces recording rules for one channel; playlists retain
// their independent selection and occurrence identities.
type SelectionChange struct {
	ChannelID      string                `json:"channel_id"`
	Revision       string                `json:"revision"`
	Selection      config.Selection      `json:"selection"`
	Content        *config.ContentPolicy `json:"content,omitempty"`
	VideoOverrides map[string]string     `json:"video_overrides,omitempty"`
}

func (a *App) proposedSelection(change SelectionChange) (config.Snapshot, config.Channel, error) {
	snapshot := a.Config.Snapshot()
	if snapshot.Revision != change.Revision {
		return snapshot, config.Channel{}, config.ErrConflict
	}

	for i := range snapshot.Config.Channels {
		ch := &snapshot.Config.Channels[i]
		if ch.ID != change.ChannelID {
			continue
		}

		ch.Indexing = &change.Selection
		ch.Content = change.Content
		ch.VideoOverrides = change.VideoOverrides

		return snapshot, *ch, ch.ValidateSelection()
	}

	return snapshot, config.Channel{}, errors.New("channel is not followed")
}

// PreviewSelection evaluates a draft against known records without source I/O.
func (a *App) PreviewSelection(ctx context.Context, change SelectionChange) (catalog.SelectionPreview, error) {
	_, ch, err := a.proposedSelection(change)
	if err != nil {
		return catalog.SelectionPreview{}, err
	}

	return a.Catalog.PreviewUploads(ctx, ch)
}

// SaveSelection saves revision-checked rules and queues enabled discovery.
func (a *App) SaveSelection(ctx context.Context, change SelectionChange) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	snapshot, ch, err := a.proposedSelection(change)
	if err != nil {
		return err
	}

	if err = a.Config.Save(snapshot.Config, change.Revision); err != nil {
		return err
	}

	if err = a.Publish(); err != nil {
		return err
	}

	if ch.Enabled {
		if err = a.Queue(ch.ID); err != nil {
			return fmt.Errorf("selection saved; channel refresh was not queued: %w", err)
		}
	}

	return nil
}

// Called under publicationMu. A changed explicit rule applies to known records
// without waiting for a source. Successful discovery later reconciles its window.
func (a *App) publishSelections(c config.Config) (bool, error) {
	current := map[string]string{}
	changed := false

	for _, ch := range c.Channels {
		active := ch.Content != nil || len(ch.VideoOverrides) > 0
		if !active && a.publicationSelections[ch.ID] == "" && a.publicationRevision != "" {
			continue
		}

		encoded, err := json.Marshal(struct {
			Selection config.Selection
			Content   *config.ContentPolicy
			Overrides map[string]string
		}{ch.Selection(), ch.Content, ch.VideoOverrides})
		if err != nil {
			return false, err
		}

		if active {
			current[ch.ID] = string(encoded)
		}

		if a.publicationSelections[ch.ID] != string(encoded) {
			if err = a.Catalog.ReconcileUploadRules(a.ctx, 0, ch); err != nil {
				return false, err
			}

			changed = true
		}
	}

	a.publicationSelections = current

	return changed, nil
}
