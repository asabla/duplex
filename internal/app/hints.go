package app

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/library"
)

var errHintManifest = errors.New("invalid library hint manifest")

type hintArtifact struct {
	Input     catalog.HintRevision `json:"input"`
	Reference string               `json:"reference"`
	Size      int64                `json:"size"`
	Modified  int64                `json:"modified"`
}

type cachedHints struct {
	artifact hintArtifact
	used     time.Time
}

func (a *App) acquireLibraryUpdate(ctx context.Context) (func(), error) {
	a.mu.Lock()
	if a.libraryUpdates == nil {
		a.libraryUpdates = make(chan struct{}, 1)
	}

	slots := a.libraryUpdates
	a.mu.Unlock()

	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *App) acquireHintWriter(ctx context.Context) (func(), error) {
	a.mu.Lock()
	if a.hintWriters == nil {
		a.hintWriters = make(chan struct{}, 1)
	}

	slots := a.hintWriters
	a.mu.Unlock()

	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *App) hintDirectory() string { return filepath.Join(a.original.IndexDir, "library") }

func (a *App) hintManifest(channel string, season int) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", channel, season)))
	return filepath.Join(a.hintDirectory(), hex.EncodeToString(hash[:])+".json")
}

func validHintReference(reference string) bool {
	value, ok := strings.CutSuffix(reference, ".plexmatch")
	if !ok || len(value) != 64 {
		return false
	}

	_, err := hex.DecodeString(value)

	return err == nil
}

func (a *App) readHintManifest(channel string, season int) (artifact hintArtifact, failure error) {
	file, err := os.Open(a.hintManifest(channel, season))
	if errors.Is(err, os.ErrNotExist) {
		return artifact, nil
	}

	if err != nil {
		return artifact, err
	}

	defer func() { failure = errors.Join(failure, file.Close()) }()

	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return artifact, err
	}

	if len(data) > 4096 || json.Unmarshal(data, &artifact) != nil || !validHintReference(artifact.Reference) || artifact.Size < 0 || artifact.Modified < 0 || artifact.Modified > time.Now().Add(24*time.Hour).Unix() {
		return hintArtifact{}, errHintManifest
	}

	return artifact, nil
}

func (a *App) verifyHintArtifact(artifact hintArtifact) (valid bool, failure error) {
	if !validHintReference(artifact.Reference) {
		return false, nil
	}

	file, err := os.Open(filepath.Join(a.hintDirectory(), artifact.Reference))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	defer func() { failure = errors.Join(failure, file.Close()) }()

	hash := sha256.New()

	size, err := io.Copy(hash, file)
	if err != nil {
		return false, err
	}

	return size == artifact.Size && hex.EncodeToString(hash.Sum(nil))+".plexmatch" == artifact.Reference, nil
}

func (a *App) cachedHints(key string, version catalog.HintRevision) (hintArtifact, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	entry, ok := a.hints[key]
	if ok && entry.artifact.Input == version {
		entry.used = time.Now()
		a.hints[key] = entry

		return entry.artifact, true
	}

	return hintArtifact{}, false
}

func (a *App) cacheHints(key string, artifact hintArtifact) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.hints == nil {
		a.hints = map[string]cachedHints{}
	}

	a.hints[key] = cachedHints{artifact: artifact, used: time.Now()}
	if len(a.hints) <= 128 {
		return
	}

	oldest := key
	for candidate, entry := range a.hints {
		if entry.used.Before(a.hints[oldest].used) {
			oldest = candidate
		}
	}

	delete(a.hints, oldest)
}

func (a *App) seasonHints(ctx context.Context, channel string, season int) (library.Entry, error) {
	release, err := a.acquireHintWriter(ctx)
	if err != nil {
		return library.Entry{}, err
	}
	defer release()

	version, err := a.Catalog.HintVersion(ctx, channel, season)
	if err != nil {
		return library.Entry{}, err
	}

	key := fmt.Sprintf("%s:%d", channel, season)
	if artifact, ok := a.cachedHints(key, version); ok {
		return a.hintEntry(artifact), nil
	}

	artifact, err := a.readHintManifest(channel, season)
	if errors.Is(err, errHintManifest) {
		a.operationFailure("library hint manifest repair", err)
		err = nil
	}

	if err != nil {
		return library.Entry{}, err
	}

	valid := false
	if artifact.Input == version {
		valid, err = a.verifyHintArtifact(artifact)
		if err != nil {
			return library.Entry{}, err
		}
	}

	if !valid {
		artifact, err = a.buildHints(ctx, channel, season, artifact)
		if err != nil {
			return library.Entry{}, err
		}
	}

	a.cacheHints(key, artifact)

	return a.hintEntry(artifact), nil
}

func (a *App) hintEntry(artifact hintArtifact) library.Entry {
	return library.Entry{Path: ".plexmatch", Modified: artifact.Modified, Media: localAsset{file: filepath.Join(a.hintDirectory(), artifact.Reference), size: artifact.Size}}
}

func (a *App) buildHints(ctx context.Context, channel string, season int, previous hintArtifact) (artifact hintArtifact, failure error) {
	defer a.Measurements.Begin("metadata.hints.build")()

	if err := os.MkdirAll(a.hintDirectory(), 0o700); err != nil {
		return artifact, err
	}

	file, err := os.CreateTemp(a.hintDirectory(), ".hints-")
	if err != nil {
		return artifact, err
	}

	closed := false
	defer func() {
		if !closed {
			failure = errors.Join(failure, file.Close())
		}

		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			failure = errors.Join(failure, err)
		}
	}()

	hash := sha256.New()
	writer := bufio.NewWriterSize(io.MultiWriter(file, hash), 32<<10)

	artifact.Input, err = a.Catalog.WalkHints(ctx, channel, season, func(hints []catalog.FileHint) error { return writeHints(writer, channel, season, hints) })
	if err != nil {
		return artifact, err
	}

	if err = writer.Flush(); err != nil {
		return artifact, err
	}

	if artifact.Size, err = file.Seek(0, io.SeekCurrent); err != nil {
		return artifact, err
	}

	if err = file.Sync(); err != nil {
		return artifact, err
	}

	closed = true

	if err = file.Close(); err != nil {
		return artifact, err
	}

	artifact.Reference = hex.EncodeToString(hash.Sum(nil)) + ".plexmatch"

	artifact.Modified = previous.Modified
	if artifact.Reference != previous.Reference {
		artifact.Modified = max(time.Now().Unix(), previous.Modified+1)
	}

	if err = os.Rename(file.Name(), filepath.Join(a.hintDirectory(), artifact.Reference)); err != nil {
		return artifact, err
	}

	encoded, err := json.Marshal(artifact)
	if err != nil {
		return artifact, err
	}

	return artifact, config.AtomicWrite(a.hintManifest(channel, season), encoded, 0o600)
}

func writeHints(writer io.Writer, channel string, season int, hints []catalog.FileHint) error {
	for _, hint := range hints {
		if hint.Number < 1 || hint.FileNumber < 1 {
			return errors.New("invalid playlist scanner slot")
		}

		name := filepath.Base(library.EpisodeBase(channel, season, hint.FileNumber))
		if _, err := fmt.Fprintf(writer, "ep: S%02dE%02d: %s.mp4\n", season, hint.Number, name); err != nil {
			return err
		}
	}

	return nil
}

func (a *App) prepareLibraryHints(ctx context.Context) error {
	for _, channel := range a.Config.Snapshot().Config.Channels {
		if !channel.Enabled || channel.Playlists == nil || !channel.Playlists.Enabled {
			continue
		}

		if err := a.prepareChannelHints(ctx, channel); err != nil {
			return err
		}
	}

	return nil
}

func (a *App) prepareChannelHints(ctx context.Context, channel config.Channel) error {
	visible := visibility(config.Config{Channels: []config.Channel{channel}})

	after := ""
	for {
		seasons, err := a.Catalog.ProjectionSeasons(ctx, visible, channel.ID, after, 128)
		if err != nil {
			return err
		}

		for _, season := range seasons {
			after = season.Name
			if strings.HasPrefix(season.Scope, "playlist:") {
				if _, err = a.seasonHints(ctx, channel.ID, season.Number); err != nil {
					return err
				}
			}
		}

		if len(seasons) < 128 {
			return nil
		}
	}
}
