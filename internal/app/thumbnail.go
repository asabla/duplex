package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"duplex/internal/artwork"
	"duplex/internal/config"
	"duplex/internal/library"
	"duplex/internal/source"
)

const artworkRefresh = 6 * time.Hour

// Only input hashes and public HTTP validators are retained, never source URLs.
type thumbnailState struct {
	Input, Raw, Output, ETag, Modified string
	Checked                            int64
}

func (a *App) thumbnailStatePath(id string) string {
	return filepath.Join(a.original.DataDir, "assets", id+".thumbnail.json")
}

func (a *App) thumbnailState(id string) thumbnailState {
	data, err := os.ReadFile(a.thumbnailStatePath(id))
	if err != nil || len(data) > 4096 {
		return thumbnailState{}
	}

	var state thumbnailState
	if json.Unmarshal(data, &state) != nil {
		return thumbnailState{}
	}

	return state
}

func (a *App) thumbnailAsset(ctx context.Context, m source.Metadata) (library.Entry, error) {
	if !assetID.MatchString(m.ID) || m.Thumbnail == "" {
		return library.Entry{}, errors.New("source thumbnail is missing")
	}

	hash := sha256.Sum256([]byte(m.Thumbnail))
	input := hex.EncodeToString(hash[:])
	previous := a.thumbnailState(m.ID)

	retained := a.retainedThumbnail(m.ID, previous.Output)

	valid := previous.Input == input && retained.Media != nil

	age := time.Since(time.Unix(previous.Checked, 0))
	if valid && age >= 0 && age < artworkRefresh {
		return retained, nil
	}

	validator := previous
	if !valid {
		validator.ETag, validator.Modified = "", ""
	}

	data, next, err := fetchThumbnail(ctx, m.Thumbnail, validator)
	if err != nil {
		return library.Entry{}, err
	}

	retained, next.Raw, err = a.decodedThumbnail(m.ID, data, previous, retained)
	if err != nil {
		return retained, err
	}

	next.Input, next.Output, next.Checked = input, retained.Media.ID(), time.Now().Unix()

	encoded, err := json.Marshal(next)
	if err != nil {
		return library.Entry{}, err
	}

	return retained, config.AtomicWriteIfChanged(a.thumbnailStatePath(m.ID), encoded, 0o600)
}

func (a *App) retainedThumbnail(id, output string) library.Entry {
	for _, entry := range a.loadAssets(id) {
		if entry.Path == ".jpg" && entry.Media != nil && entry.Media.ID() == output {
			return entry
		}
	}

	return library.Entry{}
}

func (a *App) decodedThumbnail(id string, data []byte, previous thumbnailState, retained library.Entry) (library.Entry, string, error) {
	if data == nil {
		if retained.Media == nil {
			return retained, "", errors.New("thumbnail cache validator has no retained bytes")
		}

		return retained, previous.Raw, nil
	}

	digest := sha256.Sum256(data)

	raw := hex.EncodeToString(digest[:])
	if retained.Media != nil && raw == previous.Raw {
		return retained, raw, nil
	}

	picture, err := artwork.DecodeSource(data)
	if err != nil {
		return library.Entry{}, "", err
	}

	entry, err := a.encodeThumbnail(id, picture)

	return entry, raw, err
}

func fetchThumbnail(ctx context.Context, raw string, previous thumbnailState) (data []byte, next thumbnailState, result error) {
	if !source.AllowedURL(raw) {
		return nil, next, errors.New("unsupported asset host")
	}

	req, err := httpRequest(ctx, raw)
	if err != nil {
		return nil, next, err
	}

	if previous.ETag != "" {
		req.Header.Set("If-None-Match", previous.ETag)
	}

	if previous.Modified != "" {
		req.Header.Set("If-Modified-Since", previous.Modified)
	}

	response, err := source.HTTPClient().Do(req)
	if err != nil {
		return nil, next, errors.New("thumbnail unavailable")
	}

	defer func() { result = errors.Join(result, response.Body.Close()) }()

	if response.StatusCode == http.StatusNotModified {
		if previous.ETag == "" && previous.Modified == "" {
			return nil, next, errors.New("unexpected unchanged thumbnail response")
		}

		return nil, previous, nil
	}

	if response.StatusCode != http.StatusOK {
		return nil, next, errors.New("thumbnail unavailable")
	}

	data, err = io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return nil, next, errors.New("thumbnail exceeds metadata size limit")
	}

	next.ETag, next.Modified = response.Header.Get("ETag"), response.Header.Get("Last-Modified")
	if len(next.ETag) > 512 || len(next.Modified) > 128 {
		next.ETag, next.Modified = "", ""
	}

	return data, next, nil
}
