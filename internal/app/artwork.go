package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"duplex/internal/artwork"
	"duplex/internal/catalog"
	"duplex/internal/config"
)

type artworkRecord struct {
	Reference string `json:"reference"`
	Input     string `json:"input"`
	Modified  int64  `json:"modified"`
}

// ArtworkPreview identifies the active durable asset without source URLs.
type ArtworkPreview struct {
	ChannelID string `json:"channel_id"`
	Scope     string `json:"scope"`
	Label     string `json:"label"`
	Reference string `json:"reference,omitempty"`
	Override  bool   `json:"override"`
	Path      string `json:"-"`
}

func (a *App) artworkDirectory() string { return filepath.Join(a.original.DataDir, "artwork") }
func (a *App) artworkManifest(channel, scope string) string {
	h := sha256.Sum256([]byte(channel + "\x00" + scope))
	return filepath.Join(a.artworkDirectory(), hex.EncodeToString(h[:])+".json")
}

func (a *App) automaticArtwork(channel, scope string) artworkRecord {
	data, err := os.ReadFile(a.artworkManifest(channel, scope))
	if err != nil || len(data) > 4096 {
		return artworkRecord{}
	}

	var record artworkRecord
	if json.Unmarshal(data, &record) != nil || !config.ValidArtworkReference(record.Reference) {
		return artworkRecord{}
	}

	return record
}

func (a *App) artworkChannel(id string) (config.Channel, error) {
	for _, ch := range a.Config.Snapshot().Config.Channels {
		if ch.ID == id {
			return ch, nil
		}
	}

	return config.Channel{}, errors.New("channel is not followed")
}

func (a *App) validateArtworkScope(ctx context.Context, channel, scope string) error {
	if scope == "show" {
		return nil
	}

	exists, err := a.Catalog.HasSeason(ctx, channel, scope)
	if err != nil {
		return err
	}

	if !exists {
		return errors.New("season does not exist")
	}

	return nil
}

// Artwork returns the manual override first, then the last valid automatic asset.
func (a *App) Artwork(ctx context.Context, channel, scope string) (ArtworkPreview, error) {
	result := ArtworkPreview{ChannelID: channel, Scope: scope}

	ch, err := a.artworkChannel(channel)
	if err != nil {
		return result, err
	}

	if err = a.validateArtworkScope(ctx, channel, scope); err != nil {
		return result, err
	}

	result.Label = "Channel show"

	if scope != "show" {
		seasons, err := a.ArtworkSeasons(ctx, catalog.SeasonFilter{Channel: channel, Scope: scope})
		if err != nil {
			return result, err
		}

		if len(seasons.Items) != 1 {
			return result, errors.New("season does not exist")
		}

		result.Label = seasons.Items[0].Title
	}

	ref := ch.Artwork.Seasons[scope]
	if scope == "show" {
		ref = ch.Artwork.Poster
	}

	result.Override = ref != ""
	if ref == "" {
		ref = a.automaticArtwork(channel, scope).Reference
	}

	if ref == "" {
		return result, nil
	}

	result.Path, err = artwork.Path(a.artworkDirectory(), ref)
	if err != nil {
		return result, err
	}

	if _, err = os.Stat(result.Path); err != nil {
		return result, errors.New("artwork asset is missing; restore it from backup or reset to automatic")
	}

	result.Reference = ref

	return result, nil
}

// SaveArtwork validates and re-encodes the upload before replacing the YAML ref.
func (a *App) SaveArtwork(ctx context.Context, channel, scope, revision string, reader io.Reader) error {
	if _, err := a.artworkChannel(channel); err != nil {
		return err
	}

	if err := a.validateArtworkScope(ctx, channel, scope); err != nil {
		return err
	}

	data, err := artwork.Upload(reader)
	if err != nil {
		return err
	}

	ref, err := artwork.Save(a.artworkDirectory(), data)
	if err != nil {
		return err
	}

	return a.setArtwork(ctx, channel, scope, revision, ref)
}

// ResetArtwork removes only the selected override; its durable blob is retained.
func (a *App) ResetArtwork(ctx context.Context, channel, scope, revision string) error {
	if err := a.validateArtworkScope(ctx, channel, scope); err != nil {
		return err
	}

	return a.setArtwork(ctx, channel, scope, revision, "")
}

func (a *App) setArtwork(ctx context.Context, channel, scope, revision, ref string) error {
	snap := a.Config.Snapshot()
	found := false

	for i := range snap.Config.Channels {
		ch := &snap.Config.Channels[i]
		if ch.ID != channel {
			continue
		}

		found = true

		if scope == "show" {
			ch.Artwork.Poster = ref
			continue
		}

		if ch.Artwork.Seasons == nil {
			ch.Artwork.Seasons = map[string]string{}
		}

		if ref == "" {
			delete(ch.Artwork.Seasons, scope)
		} else {
			ch.Artwork.Seasons[scope] = ref
		}
	}

	if !found {
		return errors.New("channel is not followed")
	}

	if err := a.Config.Save(snap.Config, revision); err != nil {
		return err
	}

	if err := a.Publish(); err != nil {
		return err
	}

	return a.refreshArtwork(ctx, channel)
}

func (a *App) renderArtwork(ctx context.Context, ch config.Channel) error {
	title, avatarURL, bannerURL, err := a.Catalog.Branding(ctx, ch.ID)
	if err != nil {
		return err
	}

	if title == "" {
		title = ch.ID
	}

	avatar, avatarErr := optionalPicture(ctx, avatarURL)
	banner, bannerErr := optionalPicture(ctx, bannerURL)
	failure := errors.Join(avatarErr, bannerErr)
	input := artwork.Input{Channel: title, Title: title, Label: "CHANNEL", Avatar: avatar, Banner: banner}

	changed, err := a.renderPoster(ch.ID, "show", input, failure)
	if err != nil {
		return err
	}

	seasonsChanged, err := a.renderSeasonPosters(ctx, ch.ID, input, failure)
	if err != nil {
		return err
	}

	changed = changed || seasonsChanged

	if failure != nil {
		a.operationFailure("channel branding", failure)
	}

	if !changed {
		return nil
	}

	if err = a.Publish(); err != nil {
		return err
	}

	return a.refreshArtwork(ctx, ch.ID)
}

func (a *App) renderSeasonPosters(ctx context.Context, channel string, input artwork.Input, failure error) (bool, error) {
	changed := false

	seasons, err := a.Catalog.Seasons(ctx, channel)
	if err != nil {
		return false, err
	}

	playlists, err := a.Catalog.Playlists(ctx, channel)
	if err != nil {
		return false, err
	}

	byID := map[string]catalog.Playlist{}
	for _, p := range playlists {
		byID[p.ID] = p
	}

	for scope := range seasons {
		input.Title = strings.TrimPrefix(scope, "year:")
		input.Label = "YEAR"
		input.Picture = nil
		pictureErr := failure

		if strings.HasPrefix(scope, "playlist:") {
			p := byID[strings.TrimPrefix(scope, "playlist:")]
			input.Title = p.Title
			input.Label = "PLAYLIST"
			input.Picture, pictureErr = optionalPicture(ctx, p.Thumbnail)
			pictureErr = errors.Join(failure, pictureErr)
		}

		updated, renderErr := a.renderPoster(channel, scope, input, pictureErr)
		if renderErr != nil {
			return false, renderErr
		}

		changed = changed || updated
	}

	return changed, nil
}

func optionalPicture(ctx context.Context, raw string) (image.Image, error) {
	if raw == "" {
		return nil, nil
	}

	data, err := fetchAsset(ctx, raw)
	if err != nil {
		return nil, err
	}

	return artwork.DecodeSource(data)
}

func (a *App) renderPoster(channel, scope string, input artwork.Input, sourceError error) (bool, error) {
	previous := a.automaticArtwork(channel, scope)
	if sourceError != nil && previous.Reference != "" {
		return false, nil
	}

	fingerprint, err := artwork.Fingerprint(input)
	if err != nil {
		return false, err
	}

	if previous.Input == fingerprint {
		return false, nil
	}

	data, err := artwork.Render(input)
	if err != nil {
		return false, err
	}

	ref, err := artwork.Save(a.artworkDirectory(), data)
	if err != nil {
		return false, err
	}

	record := artworkRecord{Reference: ref, Input: fingerprint, Modified: time.Now().Unix()}

	encoded, err := json.Marshal(record)
	if err != nil {
		return false, err
	}

	if err = config.AtomicWrite(a.artworkManifest(channel, scope), encoded, 0o600); err != nil {
		return false, err
	}

	return true, nil
}

func (a *App) refreshArtwork(ctx context.Context, channel string) error {
	if a.mount == nil || a.Config.Snapshot().Config.Plex.URL == "" {
		return nil
	}

	release, err := a.acquireLibraryUpdate(ctx)
	if err != nil {
		return err
	}
	defer release()

	connection := a.Config.Snapshot().Config.Plex

	client, err := a.plexClientFor(connection)
	if err != nil {
		a.setPlexError(err)
		return err
	}

	if err = client.RefreshShow(ctx, connection.Section, channel); err != nil {
		a.setPlexError(err)
		return err
	}

	return nil
}

func (a *App) artworkWorker(ctx context.Context) {
	defer a.wg.Done()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	state := artworkSchedule{brandingAt: map[string]time.Time{}, rendered: map[string]time.Time{}, versions: map[string]string{}}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		hasMedia, err := a.Catalog.HasPublished(ctx, visibility(a.Config.Snapshot().Config))
		a.operationFailure("artwork readiness", err)

		if err != nil || !hasMedia {
			continue
		}

		for _, ch := range a.Config.Snapshot().Config.Channels {
			if ch.Enabled {
				a.updateChannelArtwork(ctx, ch, &state)
			}
		}
	}
}

type artworkSchedule struct {
	brandingAt, rendered map[string]time.Time
	versions             map[string]string
}

func (a *App) updateChannelArtwork(ctx context.Context, ch config.Channel, state *artworkSchedule) {
	ctx, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()

	if time.Since(state.brandingAt[ch.ID]) >= 6*time.Hour {
		state.brandingAt[ch.ID] = time.Now()

		channel, err := a.Source.ResolveChannel(ctx, ch.URL)
		if err == nil {
			if channel.ID != ch.ID {
				err = errors.New("channel artwork identity changed")
			} else {
				err = a.Catalog.ChannelArtwork(ctx, ch.ID, channel.Avatar, channel.Banner)
			}
		}

		a.operationFailure("channel artwork source", err)
	}

	version, err := a.artworkVersion(ctx, ch.ID)
	if err == nil && (state.versions[ch.ID] != version || time.Since(state.rendered[ch.ID]) >= 30*time.Minute) {
		err = a.renderArtwork(ctx, ch)
		// Bound retry frequency after a source failure. A changed input can still
		// trigger rendering on the next worker pass.
		state.rendered[ch.ID] = time.Now()
		state.versions[ch.ID] = version
	}

	a.operationFailure("automatic posters", err)
}

func (a *App) artworkVersion(ctx context.Context, channel string) (string, error) {
	title, avatar, banner, err := a.Catalog.Branding(ctx, channel)
	if err != nil {
		return "", err
	}

	seasons, err := a.Catalog.Seasons(ctx, channel)
	if err != nil {
		return "", err
	}

	playlists, err := a.Catalog.Playlists(ctx, channel)
	if err != nil {
		return "", err
	}

	type collection struct{ ID, Title, Thumbnail string }

	selected := []collection{}
	for _, p := range playlists {
		selected = append(selected, collection{p.ID, p.Title, p.Thumbnail})
	}

	data, err := json.Marshal(struct {
		Title, Avatar, Banner string
		Seasons               map[string]int
		Playlists             []collection
	}{title, avatar, banner, seasons, selected})
	if err != nil {
		return "", err
	}

	h := sha256.Sum256(data)

	return hex.EncodeToString(h[:]), nil
}
