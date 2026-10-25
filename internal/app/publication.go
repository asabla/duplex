package app

import (
	"context"
	"time"

	"duplex/internal/diagnostics"
)

// Publish makes current durable state available through the database-backed mount.
// Directory and attribute reads use bounded queries with no catalogue snapshot.
func (a *App) Publish() error {
	defer a.Measurements.Begin("publish")()
	return a.ctx.Err()
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
