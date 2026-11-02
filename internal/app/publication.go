package app

import (
	"slices"
	"strings"
	"sync"

	"duplex/internal/config"
)

// Publication is serialized separately from runtime binding and Plex requests.
// Restart starts with no applied policies so retained directories are scanned.
type publicationState struct {
	publicationMu         sync.Mutex
	publicationRevision   string
	publicationPolicies   map[string]string
	publicationSelections map[string]string
}

// Publish makes current durable state available through the database-backed mount.
// Directory and attribute reads use bounded queries with no catalogue snapshot.
func (a *App) Publish() error {
	defer a.Measurements.Begin("publish")()

	if err := a.ctx.Err(); err != nil {
		return err
	}

	a.publicationMu.Lock()
	defer a.publicationMu.Unlock()

	snapshot := a.Config.Snapshot()
	if snapshot.Revision == a.publicationRevision {
		return nil
	}

	selectionChanged, err := a.publishSelections(snapshot.Config)
	if err != nil {
		return err
	}

	policies := projectionPolicies(snapshot.Config)

	changed := changedProjectionPolicies(a.publicationPolicies, policies)
	if len(changed) > 0 {
		if err := a.Catalog.TouchProjection(a.ctx, changed); err != nil {
			return err
		}
	}

	if len(changed) > 0 || selectionChanged || a.publicationRevision == "" {
		a.requestScan()
	}

	a.publicationRevision = snapshot.Revision
	a.publicationPolicies = policies

	return nil
}

func projectionPolicies(c config.Config) map[string]string {
	policies := map[string]string{}

	for _, channel := range c.Channels {
		if !channel.Enabled {
			continue
		}

		policy := "uploads"

		if channel.Playlists != nil && channel.Playlists.Enabled {
			skipped := []string{}

			for id, selection := range channel.Playlists.Overrides {
				if selection.Mode == "skip" {
					skipped = append(skipped, id)
				}
			}

			slices.Sort(skipped)
			policy += ":playlists:" + strings.Join(skipped, ",")
		}

		policies[channel.ID] = policy
	}

	return policies
}

func changedProjectionPolicies(previous, current map[string]string) []string {
	changed := []string{}

	for id := range previous {
		if previous[id] != current[id] {
			changed = append(changed, id)
		}
	}

	for id := range current {
		if previous[id] == "" {
			changed = append(changed, id)
		}
	}

	slices.Sort(changed)

	return changed
}
