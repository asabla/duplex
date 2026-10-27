package app

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"duplex/internal/config"
	"duplex/internal/diagnostics"
)

// Publication is serialized separately from runtime binding and Plex requests.
// Restart starts with no applied policies so retained directories are scanned.
type publicationState struct {
	publicationMu       sync.Mutex
	publicationRevision string
	publicationPolicies map[string]string
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

	policies := projectionPolicies(snapshot.Config)

	changed := changedProjectionPolicies(a.publicationPolicies, policies)
	if len(changed) > 0 {
		if err := a.Catalog.TouchProjection(a.ctx, changed); err != nil {
			return err
		}

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

type scanSchedule struct {
	lastScan time.Time
	retryAt  time.Time
	retries  int
}

func (a *App) requestScan() {
	a.mu.Lock()
	a.scanPending = true
	a.mu.Unlock()

	select {
	case a.scanWake <- struct{}{}:
	default:
	}
}

func (a *App) scanWorker(ctx context.Context) {
	defer a.wg.Done()

	timer := time.NewTicker(time.Second)
	defer timer.Stop()

	state := scanSchedule{}

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.scanWake:
		case <-timer.C:
		}

		mode, run := a.nextScan(time.Now(), &state)
		if !run {
			continue
		}

		work, stop := context.WithTimeout(ctx, 20*time.Second)

		operation := "plex.reconcile"
		if mode == integrationScan {
			operation = "plex.scan"
		}

		err := diagnostics.Run(a.Measurements, operation, func() error { return a.integration(work, mode) })

		stop()
		a.operationFailure("Plex scan", err)
		state.finished(mode, err)
	}
}

func (a *App) nextScan(now time.Time, state *scanSchedule) (integrationMode, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.scanPending && now.Sub(state.lastScan) >= 10*time.Second {
		a.scanPending = false
		state.retries = 0

		return integrationScan, true
	}

	if state.retries > 0 && !now.Before(state.retryAt) {
		state.retries--
		return integrationBindings, true
	}

	return integrationHealth, false
}

func (s *scanSchedule) finished(mode integrationMode, failure error) {
	if mode == integrationScan {
		s.lastScan = time.Now()
		if failure != nil {
			s.retries = 3
		}
	}

	if failure == nil {
		s.retries = 0
	}

	s.retryAt = time.Now().Add(30 * time.Second)
}
