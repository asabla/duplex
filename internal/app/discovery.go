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
	if err := r.discoverUploads(ctx, cursors); err != nil {
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
		if err := c.fill(ctx); err != nil {
			return nil, err
		}
		if len(c.entries) == 0 {
			return nil, nil
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

func (c *uploadCursor) fill(ctx context.Context) error {
	if len(c.entries) > 0 || c.complete {
		return nil
	}
	c.scope.Count = min(25, c.run.channel.Selection().Limit)
	listing, err := c.run.reader.Entries(ctx, c.scope, func(batch []source.Entry) error {
		if err := c.run.app.Catalog.Discover(ctx, c.run.channel.ID, batch); err != nil {
			return err
		}
		c.entries = append(c.entries, batch...)
		return nil
	})
	if err != nil {
		return err
	}
	c.complete = listing.Complete
	c.scope.Start += listing.Count
	return nil
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

func (r *discoveryRun) discoverUploads(ctx context.Context, cursors []*uploadCursor) error {
	if r.reader != nil && r.channel.Selection().Mode == "all" {
		return r.allUploads(ctx)
	}
	selected := 0
	policy := r.channel.Selection()
	for policy.Mode == "all" || selected < policy.Limit {
		best, m, err := nextUpload(ctx, cursors)
		if err != nil {
			return err
		}
		if m == nil {
			break
		}
		cursors[best].pending = cursors[best].pending[1:]
		chosen, err := r.selectUpload(ctx, *m, selected)
		if err != nil {
			return err
		}
		if chosen {
			selected++
		}
	}
	return r.finishUploads(ctx)
}

func nextUpload(ctx context.Context, cursors []*uploadCursor) (int, *source.Metadata, error) {
	heads := make([]*source.Metadata, len(cursors))
	failures := make([]error, len(cursors))
	var wg sync.WaitGroup
	for i, c := range cursors {
		wg.Add(1)
		go func() { defer wg.Done(); heads[i], failures[i] = c.next(ctx) }()
	}
	wg.Wait()
	if err := errors.Join(failures...); err != nil {
		return -1, nil, err
	}
	best := -1
	for i, m := range heads {
		if m != nil && (best < 0 || metadataBefore(*m, *heads[best])) {
			best = i
		}
	}
	if best < 0 {
		return -1, nil, nil
	}
	return best, heads[best], nil
}

func metadataBefore(a, b source.Metadata) bool {
	if a.Date().Equal(b.Date()) {
		return a.ID < b.ID
	}
	return a.Date().After(b.Date())
}

func (r *discoveryRun) selectUpload(ctx context.Context, m source.Metadata, selected int) (bool, error) {
	chosen, err := r.app.Catalog.ChooseDiscovery(ctx, r.id, "uploads", m.ID)
	if err != nil || !chosen {
		return false, err
	}
	if err = r.app.Catalog.MarkUploadedSelection(ctx, m.ID); err != nil {
		return false, err
	}
	r.app.remember(m)
	if err = r.app.queuePreparation(ctx, m.ID, max(0, 100-selected)); err != nil {
		return false, err
	}
	return true, r.app.Catalog.JobProgress(ctx, r.id, "uploads", selected+1, 0)
}

// All streams each provider tab once. Metadata and durable additions are processed
// in bounded batches; a restart re-enumerates identities and reuses saved work.
func (r *discoveryRun) allUploads(ctx context.Context) error {
	u := allUploadDiscovery{run: r}
	consume := func(batch []source.Entry) error { return u.consume(ctx, batch) }
	for _, tab := range []string{"videos", "streams", "shorts"} {
		if _, err := r.reader.Entries(ctx, source.Scope{ChannelID: r.channel.ID, Tab: tab, Start: 1}, consume); err != nil {
			return err
		}
	}
	for _, id := range r.channel.Unlisted {
		if err := consume([]source.Entry{{ID: id}}); err != nil {
			return err
		}
	}
	return r.finishUploads(ctx)
}

type allUploadDiscovery struct {
	run                *discoveryRun
	selected, position int
}

func (u *allUploadDiscovery) consume(ctx context.Context, batch []source.Entry) error {
	for start := 0; start < len(batch); start += 4 {
		values, err := u.run.metadataBatch(ctx, batch[start:min(start+4, len(batch))], true)
		if err != nil {
			return err
		}
		for _, m := range values {
			if err = u.add(ctx, m); err != nil {
				return err
			}
		}
	}
	return nil
}

func (u *allUploadDiscovery) add(ctx context.Context, m source.Metadata) error {
	u.position++
	if err := u.run.app.Catalog.StageDiscovery(ctx, u.run.id, "uploads", m.ID, u.position); err != nil {
		return err
	}
	if !recording(m) {
		return nil
	}
	chosen, err := u.run.selectUpload(ctx, m, u.selected)
	if chosen {
		u.selected++
	}
	return err
}

func (r *discoveryRun) finishUploads(ctx context.Context) error {
	limit := 0
	if policy := r.channel.Selection(); policy.Mode == "latest" {
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
