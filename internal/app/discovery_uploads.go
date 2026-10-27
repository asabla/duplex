package app

import (
	"context"
	"errors"
	"slices"
	"sync"

	"duplex/internal/config"
	"duplex/internal/source"
)

type uploadCursor struct {
	run      *discoveryRun
	scope    source.Scope
	entries  []source.Entry
	pending  []source.Metadata
	complete bool
	position int
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

	c.scope.Count = min(25, c.run.channel.ContentSelection(source.ContentKind(c.scope.Tab)).Limit)

	listing, err := c.run.reader.Entries(ctx, c.scope, func(batch []source.Entry) error {
		for i := range batch {
			batch[i].Kind = source.ContentKind(c.scope.Tab)
		}

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

func (r *discoveryRun) discoverUploads(ctx context.Context, cursors []*uploadCursor) error {
	if r.channel.Content != nil {
		if r.reader == nil {
			return errors.New("separate content selection requires source tab discovery")
		}

		return r.separateUploads(ctx, cursors)
	}

	if r.reader != nil && r.channel.Selection().Mode == "all" {
		return r.allUploads(ctx)
	}

	if err := r.uploadWindow(ctx, cursors, r.channel.Selection()); err != nil {
		return err
	}

	return r.finishUploads(ctx)
}

func (r *discoveryRun) uploadWindow(ctx context.Context, cursors []*uploadCursor, policy config.Selection) error {
	selected := 0
	for policy.Mode == "all" || selected < policy.Limit {
		best, m, err := nextUpload(ctx, cursors)
		if err != nil {
			return err
		}

		if m == nil {
			break
		}

		cursors[best].pending = cursors[best].pending[1:]

		if r.channel.Content != nil {
			kind, failure := r.app.Catalog.RecordingKind(ctx, m.ID)
			if failure != nil {
				return failure
			}

			if kind != source.ContentKind(cursors[best].scope.Tab) {
				continue
			}
		}

		chosen, err := r.selectUpload(ctx, *m, selected)
		if err != nil {
			return err
		}

		if chosen {
			selected++
		}
	}

	return nil
}

func nextUpload(ctx context.Context, cursors []*uploadCursor) (int, *source.Metadata, error) {
	heads := make([]*source.Metadata, len(cursors))
	failures := make([]error, len(cursors))

	var wg sync.WaitGroup
	for i, c := range cursors {
		wg.Add(1)
		go func() {
			defer wg.Done()

			heads[i], failures[i] = c.next(ctx)
		}()
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
	r.app.publicationMu.Lock()
	defer r.app.publicationMu.Unlock()

	if err := r.current(); err != nil {
		return false, err
	}

	if r.channel.VideoOverrides[m.ID] == "exclude" {
		return false, nil
	}

	if r.channel.Content != nil && r.channel.VideoOverrides[m.ID] != "keep" {
		kind, err := r.app.Catalog.RecordingKind(ctx, m.ID)
		if err != nil || r.channel.ContentSelection(kind).Mode == "skip" {
			return false, err
		}
	}

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

	for _, tab := range []string{"videos", "streams", "shorts"} {
		if _, err := r.reader.Entries(ctx, source.Scope{ChannelID: r.channel.ID, Tab: tab, Start: 1}, func(batch []source.Entry) error {
			for i := range batch {
				batch[i].Kind = source.ContentKind(tab)
			}

			return u.consume(ctx, batch)
		}); err != nil {
			return err
		}
	}

	for _, id := range r.channel.Unlisted {
		if err := u.consume(ctx, []source.Entry{{ID: id}}); err != nil {
			return err
		}
	}

	return r.finishUploads(ctx)
}

type allUploadDiscovery struct {
	run                *discoveryRun
	selected, position int
	kind               string
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

	if u.kind != "" {
		kind, err := u.run.app.Catalog.RecordingKind(ctx, m.ID)
		if err != nil {
			return err
		}

		if kind != u.kind {
			return nil
		}
	}

	chosen, err := u.run.selectUpload(ctx, m, u.selected)
	if chosen {
		u.selected++
	}

	return err
}

func (r *discoveryRun) finishUploads(ctx context.Context) error {
	if err := r.keptUploads(ctx); err != nil {
		return err
	}

	if err := r.commitUploadSelection(ctx); err != nil {
		return err
	}

	return r.app.Publish()
}

func (r *discoveryRun) commitUploadSelection(ctx context.Context) error {
	r.app.publicationMu.Lock()
	defer r.app.publicationMu.Unlock()

	if err := r.current(); err != nil {
		return err
	}

	// Shared policies also need a durable checkpoint after explicit rules are
	// cleared, including later edits to that shared policy.
	return r.app.Catalog.ReconcileUploadRules(ctx, r.id, r.channel)
}

func (r *discoveryRun) separateUploads(ctx context.Context, cursors []*uploadCursor) error {
	for _, c := range cursors {
		kind := source.ContentKind(c.scope.Tab)
		if kind == "" {
			continue
		}

		policy := r.channel.ContentSelection(kind)
		if policy.Mode == "skip" {
			continue
		}

		if policy.Mode == "all" {
			u := allUploadDiscovery{run: r, kind: kind}
			if _, err := r.reader.Entries(ctx, c.scope, func(batch []source.Entry) error {
				for i := range batch {
					batch[i].Kind = kind
				}

				return u.consume(ctx, batch)
			}); err != nil {
				return err
			}
		} else if err := r.uploadWindow(ctx, []*uploadCursor{c}, policy); err != nil {
			return err
		}
	}

	if err := r.explicitUploads(ctx, r.channel.Unlisted); err != nil {
		return err
	}

	return r.finishUploads(ctx)
}

func (r *discoveryRun) keptUploads(ctx context.Context) error {
	ids := []string{}

	for id, mode := range r.channel.VideoOverrides {
		if mode == "keep" {
			ids = append(ids, id)
		}
	}

	slices.Sort(ids)

	return r.explicitUploads(ctx, ids)
}

func (r *discoveryRun) explicitUploads(ctx context.Context, ids []string) error {
	u := allUploadDiscovery{run: r}
	for _, id := range ids {
		if err := u.consume(ctx, []source.Entry{{ID: id}}); err != nil {
			return err
		}
	}

	return nil
}
