package app

import (
	"context"
	"errors"
	"sort"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/source"
)

func (r *discoveryRun) playlists(ctx context.Context) error {
	if r.channel.Playlists == nil || !r.channel.Playlists.Enabled {
		return nil
	}
	if err := r.discoverPlaylists(ctx); err != nil {
		return err
	}
	if err := r.current(); err != nil {
		return err
	}
	if err := r.app.Catalog.ReconcilePlaylists(ctx, r.id, r.channel.ID); err != nil {
		return err
	}
	after := ""
	var failures []error
	for {
		playlists, err := r.app.Catalog.DiscoveredPlaylists(ctx, r.id, r.channel.ID, after, 100)
		if err != nil {
			return err
		}
		for _, p := range playlists {
			after = p.ID
			if err = r.refreshPlaylist(ctx, p.ID); err != nil {
				failures = append(failures, err)
				if ctx.Err() != nil {
					return errors.Join(failures...)
				}
			}
		}
		if len(playlists) < 100 {
			break
		}
	}
	return errors.Join(append(failures, r.app.Publish())...)
}

func (r *discoveryRun) refreshPlaylist(ctx context.Context, id string) error {
	policy := r.channel.PlaylistSelection(id)
	if policy.Mode == "skip" {
		return nil
	}
	return r.playlist(ctx, id, policy)
}

func (r *discoveryRun) discoverPlaylists(ctx context.Context) error {
	consume := func(batch []source.Playlist) error {
		for _, p := range batch {
			if p.ChannelID != "" && p.ChannelID != r.channel.ID {
				return errors.New("playlist owner changed")
			}
			if err := r.app.Catalog.DiscoverPlaylist(ctx, r.id, catalog.Playlist{ID: p.ID, ChannelID: r.channel.ID, Title: p.Title, Description: p.Description, Thumbnail: p.Thumbnail}); err != nil {
				return err
			}
		}
		return nil
	}
	if stream, ok := r.reader.(source.PlaylistStreamer); ok {
		return stream.StreamPlaylists(ctx, r.channel.ID, consume)
	}
	playlists, err := r.reader.Playlists(ctx, r.channel.ID)
	if err != nil {
		return err
	}
	return consume(playlists)
}

type playlistDiscovery struct {
	run      *discoveryRun
	id       string
	policy   config.Selection
	frontier []source.Metadata
}

func (r *discoveryRun) playlist(ctx context.Context, id string, policy config.Selection) error {
	p := playlistDiscovery{run: r, id: id, policy: policy}
	_, err := r.reader.Entries(ctx, source.Scope{PlaylistID: id, Start: 1}, func(batch []source.Entry) error { return p.consume(ctx, batch) })
	if err != nil {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
		defer stop()
		return errors.Join(err, r.app.Catalog.FailPlaylist(cleanup, id))
	}
	limit := 0
	if policy.Mode == "latest" {
		limit = policy.Limit
	}
	if err = r.current(); err != nil {
		return err
	}
	release, err := r.app.acquireLibraryUpdate(ctx)
	if err != nil {
		return err
	}
	defer release()
	return r.app.Catalog.ReconcileDiscovery(ctx, r.id, id, r.channel.ID, limit)
}

func (p *playlistDiscovery) consume(ctx context.Context, batch []source.Entry) error {
	for start := 0; start < len(batch); start += 4 {
		entries := batch[start:min(start+4, len(batch))]
		values, err := p.run.metadataBatch(ctx, entries, false)
		if err != nil {
			return err
		}
		for i, m := range values {
			if err = p.add(ctx, entries[i], m); err != nil {
				return err
			}
		}
	}
	n, err := p.run.app.Catalog.DiscoveryPosition(ctx, p.run.id, p.id)
	if err != nil {
		return err
	}
	return p.run.app.Catalog.JobProgress(ctx, p.run.id, "playlist "+p.id, n, 0)
}

func (p *playlistDiscovery) add(ctx context.Context, entry source.Entry, m source.Metadata) error {
	if err := p.run.app.Catalog.StageDiscovery(ctx, p.run.id, p.id, m.ID, entry.Position); err != nil {
		return err
	}
	if !recording(m) || !p.choose(m) {
		return nil
	}
	if err := p.run.app.Catalog.AddPlaylistMember(ctx, p.id, m.ID, entry.Position, true); err != nil {
		return err
	}
	p.run.app.remember(m)
	return p.run.app.queuePreparation(ctx, m.ID, 0)
}

func (p *playlistDiscovery) choose(m source.Metadata) bool {
	if p.policy.Mode == "all" {
		return true
	}
	// Arbitrary playlist order requires complete enumeration, with only the
	// eligible Latest frontier retained in memory.
	for _, candidate := range p.frontier {
		if candidate.ID == m.ID {
			return true
		}
	}
	p.frontier = append(p.frontier, source.Metadata{ID: m.ID, Timestamp: float64(m.Date().Unix())})
	sort.Slice(p.frontier, func(i, j int) bool { return metadataBefore(p.frontier[i], p.frontier[j]) })
	if len(p.frontier) > p.policy.Limit {
		p.frontier = p.frontier[:p.policy.Limit]
	}
	for _, candidate := range p.frontier {
		if candidate.ID == m.ID {
			return true
		}
	}
	return false
}
