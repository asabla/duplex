package app

import (
	"context"
	"errors"
	"sync"

	"duplex/internal/source"
)

func (r *discoveryRun) metadataBatch(ctx context.Context, entries []source.Entry, owned bool) ([]source.Metadata, error) {
	if err := r.app.Catalog.Discover(ctx, r.channel.ID, entries); err != nil {
		return nil, err
	}

	out := make([]source.Metadata, len(entries))
	failures := make([]error, len(entries))

	var wg sync.WaitGroup
	for i, entry := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()

			out[i], failures[i] = r.metadata(ctx, entry, owned)
		}()
	}

	wg.Wait()

	return out, errors.Join(failures...)
}

func (r *discoveryRun) metadata(ctx context.Context, entry source.Entry, owned bool) (source.Metadata, error) {
	m, err := r.app.Catalog.CachedMetadata(ctx, entry.ID)

	v, videoErr := r.app.Catalog.Video(ctx, entry.ID)
	if videoErr != nil {
		return source.Metadata{}, videoErr
	}

	cached := err == nil && videoErr == nil && recording(m) && v.MetadataAt > 0 && r.app.metadataFresh(v, entry)
	if !cached {
		m, err = r.app.Source.Metadata(ctx, entry.ID)
		if errors.Is(err, source.ErrUnavailable) {
			m = source.Metadata{ID: entry.ID, ChannelID: v.ChannelID, Title: entry.Title, LiveStatus: "unavailable"}
			return m, r.app.Catalog.MarkUnavailable(ctx, entry.ID)
		}

		if err != nil {
			return m, err
		}
	}

	if m.ID != entry.ID || m.ChannelID == "" || (owned && m.ChannelID != r.channel.ID) {
		return m, errors.New("source video or uploader identity changed")
	}

	if !cached {
		if err = r.app.Catalog.SaveMetadata(ctx, m); err != nil {
			return m, err
		}
	}

	return m, nil
}
