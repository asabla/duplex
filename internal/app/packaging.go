package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/diagnostics"
	"duplex/internal/media"
	"duplex/internal/source"
)

// Prepare verifies the highest supported candidate and records any quality fallback.
// Optional assets run separately; no media payload is prepared as a complete file.
func (a *App) Prepare(ctx context.Context, m source.Metadata, maxHeight int) (catalog.Prepared, error) {
	prepared, _, err := a.prepareWithRepresentation(ctx, m, maxHeight)
	return prepared, err
}

func (a *App) prepareWithRepresentation(ctx context.Context, m source.Metadata, maxHeight int) (catalog.Prepared, media.Representation, error) {
	release, err := a.acquirePreparation(ctx)
	if err != nil {
		return catalog.Prepared{}, nil, err
	}
	defer release()
	defer a.Measurements.Begin("prepare")()
	if m.Date().IsZero() {
		return catalog.Prepared{}, nil, errors.New("source publication date is unavailable")
	}
	failure := ""
	var causes []error
	attempts := 0
	for _, candidate := range source.Candidates(m, maxHeight) {
		if !candidate.Combined && attempts >= 6 {
			continue
		}
		attempts++
		prepared, rep, err := a.prepareCandidate(ctx, m, candidate, maxHeight)
		if err == nil {
			prepared.Fallback = failure
			return prepared, rep, nil
		}
		causes = append(causes, err)
		if failure == "" {
			failure = fallbackReason(err)
		}
	}
	if len(causes) == 0 {
		return catalog.Prepared{}, nil, media.ErrUnsupported
	}
	return catalog.Prepared{}, nil, fmt.Errorf("no verified supported representation: %w", errors.Join(causes...))
}

func (a *App) mediaClient() *http.Client {
	if a.httpClient != nil {
		return a.httpClient
	}
	return source.HTTPClient()
}

func (a *App) openTrack(ctx context.Context, id string, f source.Format) (*media.HTTPSource, error) {
	return diagnostics.Measure(a.Measurements, "media.probe", func() (*media.HTTPSource, error) {
		return media.OpenVersionedHTTP(ctx, a.mediaClient(), f.URL, id+":"+f.ID, a.Cache, media.SourceVersion{Key: f.Revision, Size: f.VerifiedSize})
	})
}

func (a *App) prepareCandidate(ctx context.Context, m source.Metadata, candidate source.Candidate, maxHeight int) (catalog.Prepared, media.Representation, error) {
	buildCtx, stop := context.WithTimeout(ctx, 5*time.Minute)
	defer stop()
	representation, indexPath, err := a.candidateRepresentation(buildCtx, m, candidate)
	if err != nil {
		return catalog.Prepared{}, nil, err
	}
	if err = diagnostics.Run(a.Measurements, "media.verify", func() error { return media.VerifyRanges(buildCtx, representation) }); err != nil {
		return catalog.Prepared{}, nil, err
	}
	inspected, ok := representation.(media.Inspected)
	if !ok || inspected.Details().Height <= 0 || inspected.Details().Height > maxHeight {
		return catalog.Prepared{}, nil, media.ErrUnsupported
	}
	if err = a.persistRepresentation(m, candidate, inspected, indexPath); err != nil {
		return catalog.Prepared{}, nil, err
	}
	a.cacheRepresentation(representation, leaseExpiry(m))
	return catalog.Prepared{RequestedHeight: maxHeight, Metadata: m, Revision: representation.ID(), Height: inspected.Details().Height, Size: representation.Size()}, representation, nil
}

func (a *App) candidateRepresentation(ctx context.Context, m source.Metadata, candidate source.Candidate) (media.Representation, string, error) {
	video, err := a.openTrack(ctx, m.ID, candidate.Video)
	if err != nil {
		return nil, "", err
	}
	if candidate.Combined {
		details, err := media.InspectCombined(ctx, video)
		return media.Described{Representation: video, Info: details}, "", err
	}
	if candidate.Audio == nil {
		return nil, "", media.ErrUnsupported
	}
	audio, err := a.openTrack(ctx, m.ID, *candidate.Audio)
	if err != nil {
		return nil, "", err
	}
	key := sha256.Sum256([]byte(m.ID + ":" + candidate.Video.ID + ":" + candidate.Audio.ID))
	indexPath := filepath.Join(a.original.IndexDir, hex.EncodeToString(key[:])+".json")
	index, err := diagnostics.Measure(a.Measurements, "media.index.load", func() (*media.VirtualMP4, error) {
		return media.LoadIndex(indexPath, map[string]media.Representation{video.ID(): video, audio.ID(): audio})
	})
	if err == nil && index.Details().Height > 0 {
		return index, indexPath, nil
	}
	index, err = diagnostics.Measure(a.Measurements, "media.index.build", func() (*media.VirtualMP4, error) { return media.Mux(ctx, video, audio) })
	return index, indexPath, err
}

func (a *App) persistRepresentation(m source.Metadata, candidate source.Candidate, representation media.Inspected, indexPath string) error {
	if index, ok := representation.(*media.VirtualMP4); ok {
		if err := index.SaveIndex(filepath.Join(a.original.IndexDir, representation.ID()+".mp4-index.json")); err != nil {
			return err
		}
		if err := index.SaveIndex(indexPath); err != nil {
			return err
		}
	}
	audio := ""
	if candidate.Audio != nil {
		audio = candidate.Audio.ID
	}
	descriptor := struct {
		VideoID, Revision, VideoFormat, AudioFormat string
		Size                                        int64
		Media                                       media.Details
	}{m.ID, representation.ID(), candidate.Video.ID, audio, representation.Size(), representation.Details()}
	data, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}
	return config.AtomicWrite(filepath.Join(a.original.IndexDir, representation.ID()+".description.json"), data, 0o600)
}
