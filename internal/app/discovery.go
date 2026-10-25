package app

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/source"
)

type (
	discoveryRun struct {
		app     *App
		id      int64
		channel config.Channel
		reader  source.Collections
	}
	uploadCursor struct {
		run      *discoveryRun
		scope    source.Scope
		entries  []source.Entry
		pending  []source.Metadata
		complete bool
		position int
	}
)

// Sync records one discovery job and reconciles only successfully enumerated windows.
func (a *App) Sync(ctx context.Context, id string) error {
	defer a.Measurements.Begin("sync")()
	ch, err := a.followed(id)
	if err != nil {
		return err
	}
	runID, err := a.Catalog.StartJob(ctx, id)
	if err != nil {
		return err
	}
	run := discoveryRun{app: a, id: runID, channel: ch}
	run.reader, _ = source.CollectionReader(a.Source)
	err = run.execute(ctx)
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer stop()
	finishErr := a.Catalog.FinishJob(cleanup, runID, id, err)
	return errors.Join(err, finishErr)
}

func (a *App) followed(id string) (config.Channel, error) {
	for _, ch := range a.Config.Snapshot().Config.Channels {
		if ch.ID == id && ch.Enabled {
			return ch, nil
		}
	}
	return config.Channel{}, errors.New("channel is not followed or enabled")
}

func (r *discoveryRun) execute(ctx context.Context) error {
	r.app.mu.RLock()
	toolError := r.app.toolError
	r.app.mu.RUnlock()
	if toolError != "" {
		return errors.New(toolError)
	}
	if err := r.app.Catalog.EnsureChannel(ctx, r.channel.ID, ""); err != nil {
		return err
	}
	var cursors []*uploadCursor
	var title string
	if r.reader != nil {
		for _, tab := range []string{"videos", "streams", "shorts"} {
			cursors = append(cursors, &uploadCursor{run: r, scope: source.Scope{ChannelID: r.channel.ID, Tab: tab, Start: 1}})
		}
	} else {
		snapshot, err := r.app.Source.Enumerate(ctx, r.channel.ID)
		if err != nil {
			return err
		}
		if snapshot.ID != r.channel.ID {
			return errors.New("channel identity changed")
		}
		title = snapshot.Title
		groups := map[int][]source.Entry{}
		for _, entry := range snapshot.Entries {
			groups[entry.Group] = append(groups[entry.Group], entry)
		}
		for _, entries := range groups {
			cursors = append(cursors, &uploadCursor{run: r, entries: entries, complete: true})
		}
	}
	if len(r.channel.Unlisted) > 0 {
		explicit := &uploadCursor{run: r, complete: true}
		for _, id := range r.channel.Unlisted {
			explicit.entries = append(explicit.entries, source.Entry{ID: id})
		}
		cursors = append(cursors, explicit)
	}
	if err := r.uploads(ctx, cursors); err != nil {
		return err
	}
	if err := r.app.Catalog.EnsureChannel(ctx, r.channel.ID, title); err != nil {
		return err
	}
	if r.reader != nil {
		return r.playlists(ctx)
	}
	return nil
}

func (c *uploadCursor) next(ctx context.Context) (*source.Metadata, error) {
	for len(c.pending) == 0 {
		if len(c.entries) == 0 {
			if c.complete {
				return nil, nil
			}
			c.scope.Count = 25
			if p := c.run.channel.Selection(); p.Mode == "latest" {
				c.scope.Count = min(25, p.Limit)
			}
			listing, err := c.run.reader.Entries(ctx, c.scope, func(batch []source.Entry) error {
				if err := c.run.app.Catalog.Discover(ctx, c.run.channel.ID, batch); err != nil {
					return err
				}
				c.entries = append(c.entries, batch...)
				return nil
			})
			if err != nil {
				return nil, err
			}
			c.complete = listing.Complete
			c.scope.Start += listing.Count
			if len(c.entries) == 0 {
				return nil, nil
			}
		}
		n := min(4, len(c.entries))
		batch := c.entries[:n]
		c.entries = c.entries[n:]
		values, err := c.run.metadataBatch(ctx, batch, true)
		if err != nil {
			return nil, err
		}
		for i, m := range values {
			c.position++
			if err = c.run.app.Catalog.StageDiscovery(ctx, c.run.id, "uploads", batch[i].ID, c.position); err != nil {
				return nil, err
			}
			if recording(m) {
				c.pending = append(c.pending, m)
			}
		}
	}
	return &c.pending[0], nil
}

func (r *discoveryRun) metadataBatch(ctx context.Context, entries []source.Entry, owned bool) ([]source.Metadata, error) {
	if err := r.app.Catalog.Discover(ctx, r.channel.ID, entries); err != nil {
		return nil, err
	}
	out := make([]source.Metadata, len(entries))
	failures := make([]error, len(entries))
	var wg sync.WaitGroup
	for i, entry := range entries {
		wg.Add(1)
		go func() { defer wg.Done(); out[i], failures[i] = r.metadata(ctx, entry, owned) }()
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
		if m.ID != entry.ID || m.ChannelID == "" || (owned && m.ChannelID != r.channel.ID) {
			return m, errors.New("source video or uploader identity changed")
		}
		if err = r.app.Catalog.SaveMetadata(ctx, m); err != nil {
			return m, err
		}
	}
	return m, nil
}

func (r *discoveryRun) uploads(ctx context.Context, cursors []*uploadCursor) error {
	policy := r.channel.Selection()
	selected := 0
	for policy.Mode == "all" || selected < policy.Limit {
		heads := make([]*source.Metadata, len(cursors))
		failures := make([]error, len(cursors))
		var wg sync.WaitGroup
		for i, c := range cursors {
			wg.Add(1)
			go func() { defer wg.Done(); heads[i], failures[i] = c.next(ctx) }()
		}
		wg.Wait()
		if err := errors.Join(failures...); err != nil {
			return err
		}
		best := -1
		for i, m := range heads {
			if m != nil && (best < 0 || m.Date().After(heads[best].Date()) || (m.Date().Equal(heads[best].Date()) && m.ID < heads[best].ID)) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		m := *heads[best]
		cursors[best].pending = cursors[best].pending[1:]
		chosen, err := r.app.Catalog.ChooseDiscovery(ctx, r.id, "uploads", m.ID)
		if err != nil {
			return err
		}
		if !chosen {
			continue
		}
		if err := r.app.Catalog.MarkUploadedSelection(ctx, m.ID); err != nil {
			return err
		}
		r.app.remember(m)
		if err := r.app.queuePreparation(ctx, m.ID, 100-selected); err != nil {
			return err
		}
		selected++
		if err := r.app.Catalog.JobProgress(ctx, r.id, "uploads", selected, 0); err != nil {
			return err
		}
	}
	limit := 0
	if policy.Mode == "latest" {
		limit = policy.Limit
	}
	if err := r.current(); err != nil {
		return err
	}
	if err := r.app.Catalog.ReconcileDiscovery(ctx, r.id, "uploads", r.channel.ID, limit); err != nil {
		return err
	}
	return r.app.Publish()
}

func (r *discoveryRun) playlists(ctx context.Context) error {
	if r.channel.Playlists == nil || !r.channel.Playlists.Enabled {
		return nil
	}
	playlists, err := r.reader.Playlists(ctx, r.channel.ID)
	if err != nil {
		return err
	}
	for _, p := range playlists {
		if err = r.app.Catalog.SavePlaylist(ctx, catalog.Playlist{ID: p.ID, ChannelID: r.channel.ID, Title: p.Title, Description: p.Description, Thumbnail: p.Thumbnail}); err != nil {
			return err
		}
	}
	for _, p := range playlists {
		policy := r.channel.PlaylistSelection(p.ID)
		if policy.Mode == "skip" {
			continue
		}
		if err = r.playlist(ctx, p.ID, policy); err != nil {
			return err
		}
	}
	return r.app.Publish()
}

func (r *discoveryRun) playlist(ctx context.Context, id string, policy config.Selection) error {
	// Playlist order is arbitrary. Latest must inspect the complete collection;
	// only the bounded eligible frontier is held in memory.
	frontier := []source.Metadata{}
	_, err := r.reader.Entries(ctx, source.Scope{PlaylistID: id, Start: 1}, func(batch []source.Entry) error {
		for start := 0; start < len(batch); start += 4 {
			entries := batch[start:min(start+4, len(batch))]
			values, e := r.metadataBatch(ctx, entries, false)
			if e != nil {
				return e
			}
			for i, m := range values {
				entry := entries[i]
				if e = r.app.Catalog.StageDiscovery(ctx, r.id, id, m.ID, entry.Position); e != nil {
					return e
				}
				if !recording(m) {
					continue
				}
				chosen := policy.Mode == "all"
				if policy.Mode == "latest" {
					frontier = append(frontier, source.Metadata{ID: m.ID, Timestamp: float64(m.Date().Unix())})
					sort.Slice(frontier, func(i, j int) bool {
						if frontier[i].Date().Equal(frontier[j].Date()) {
							return frontier[i].ID < frontier[j].ID
						}
						return frontier[i].Date().After(frontier[j].Date())
					})
					if len(frontier) > policy.Limit {
						frontier = frontier[:policy.Limit]
					}
					for _, candidate := range frontier {
						if candidate.ID == m.ID {
							chosen = true
							break
						}
					}
				}
				if chosen {
					if e = r.app.Catalog.AddPlaylistMember(ctx, id, m.ID, entry.Position, true); e != nil {
						return e
					}
					r.app.remember(m)
					if e = r.app.queuePreparation(ctx, m.ID, 0); e != nil {
						return e
					}
				}
			}
		}
		n, e := r.app.Catalog.DiscoveryPosition(ctx, r.id, id)
		if e != nil {
			return e
		}
		return r.app.Catalog.JobProgress(ctx, r.id, "playlist "+id, n, 0)
	})
	if err != nil {
		return err
	}
	limit := 0
	if policy.Mode == "latest" {
		limit = policy.Limit
	}
	if err = r.current(); err != nil {
		return err
	}
	return r.app.Catalog.ReconcileDiscovery(ctx, r.id, id, r.channel.ID, limit)
}

func (r *discoveryRun) current() error {
	ch, err := r.app.followed(r.channel.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(ch, r.channel) {
		return errors.New("channel settings changed during discovery; refresh queued settings")
	}
	return nil
}
