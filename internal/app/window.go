package app

import (
	"context"
	"errors"
	"sort"

	"duplex/internal/catalog"
	"duplex/internal/source"
)

// prepareWindow resolves per-tab metadata windows before packaging the global
// newest recordings. Source channel tabs enumerate newest first. A failed
// representation refills its own tab frontier before an older tab can win.
// Enumeration has already completed; metadata failures abort the staged snapshot.
func prepareWindow(ctx context.Context, adapter source.SourceAdapter, snapshot source.Channel, limit int, prepare func(source.Metadata) (catalog.Prepared, error)) ([]catalog.Prepared, error) {
	groups := map[int][]source.Entry{}
	order := []int{}
	for _, entry := range snapshot.Entries {
		if _, ok := groups[entry.Group]; !ok {
			order = append(order, entry.Group)
		}
		groups[entry.Group] = append(groups[entry.Group], entry)
	}
	type candidate struct {
		metadata source.Metadata
		group    int
	}
	positions := map[int]int{}
	result := []catalog.Prepared{}
	pending := []candidate{}
	readNext := func(group, count int) error {
		eligible := 0
		for positions[group] < len(groups[group]) && (eligible < count || group == -1) {
			entry := groups[group][positions[group]]
			positions[group]++
			if e := ctx.Err(); e != nil {
				return e
			}
			m, e := adapter.Metadata(ctx, entry.ID)
			if errors.Is(e, source.ErrUnavailable) {
				m = source.Metadata{ID: entry.ID, ChannelID: snapshot.ID, Title: entry.Title, LiveStatus: "unavailable"}
				e = nil
			}
			if e != nil {
				return e
			}
			if m.ID != entry.ID || m.ChannelID != snapshot.ID {
				return errors.New("source video or channel identity changed")
			}
			if m.State() == "recorded" || m.State() == "replay-ready" {
				pending = append(pending, candidate{m, group})
				eligible++
			} else {
				result = append(result, catalog.Prepared{Metadata: m})
			}
		}
		return nil
	}
	for _, group := range order {
		if e := readNext(group, limit); e != nil {
			return nil, e
		}
	}
	ready := 0
	for len(pending) > 0 && ready < limit {
		sort.SliceStable(pending, func(i, j int) bool {
			a, b := pending[i].metadata.Date(), pending[j].metadata.Date()
			if a.Equal(b) {
				return pending[i].metadata.ID < pending[j].metadata.ID
			}
			return a.After(b)
		})
		next := pending[0]
		pending = pending[1:]
		p, e := prepare(next.metadata)
		if e != nil {
			return nil, e
		}
		result = append(result, p)
		if p.Revision != "" {
			ready++
		} else if e = readNext(next.group, 1); e != nil {
			return nil, e
		}
	}
	return result, nil
}
