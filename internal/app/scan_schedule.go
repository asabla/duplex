package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/diagnostics"
)

var errScanPending = errors.New("plex library scan is in progress")

const (
	scanQuiet        = 30 * time.Second
	scanMaximumDelay = 2 * time.Minute
)

// Only the worker owns this state. Pending publication is protected by App.mu.
type scanSchedule struct {
	retryAt   time.Time
	awaiting  bool
	retryScan bool
	targets   []string
}

func (a *App) requestScan() { a.requestChannelScan("") }

func (a *App) requestChannelScan(channel string) {
	a.mu.Lock()
	if !a.scanPending {
		a.scanFirst = time.Now()
	}

	a.scanPending, a.scanLast = true, time.Now()
	if channel == "" {
		a.scanFull = true
	}

	if a.scanChannels == nil {
		a.scanChannels = map[string]bool{}
	}

	if len(a.scanChannels) < 64 {
		a.scanChannels[channel] = true
	} else {
		a.scanFull = true
	}

	a.mu.Unlock()

	select {
	case a.scanWake <- struct{}{}:
	default:
	}
}

// Shared canonical recordings can appear under other playlist owners. Keep the
// narrow request only when uploads are the sole projection.
func (a *App) requestRecordingScan(channel string) {
	for _, ch := range a.Config.Snapshot().Config.Channels {
		if ch.Enabled && ch.Playlists != nil && ch.Playlists.Enabled {
			a.requestScan()
			return
		}
	}

	a.requestChannelScan(channel)
}

func (a *App) recordVideoPublication(ctx context.Context, before catalog.Video) error {
	after, err := a.Catalog.Video(ctx, before.ID)
	if err != nil {
		return err
	}

	if after.Updated != before.Updated || after.Revision != before.Revision {
		a.requestRecordingScan(after.ChannelID)
	}

	return nil
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

		if !a.automaticUpdatesAllowed() {
			continue
		}

		mode, run := a.nextScan(time.Now(), &state)
		if !run {
			continue
		}

		work, stop := context.WithTimeout(ctx, 20*time.Second)
		err := diagnostics.Run(a.Measurements, "plex.reconcile", func() error { return a.integrationTargets(work, mode, state.targets) })

		stop()

		if !errors.Is(err, errBackgroundDeferred) && !errors.Is(err, errScanPending) {
			a.operationFailure("Plex scan", err)
		}

		state.finished(mode, err, time.Now())
		a.recordScanState(&state)
	}
}

func (a *App) nextScan(now time.Time, state *scanSchedule) (integrationMode, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if now.Before(state.retryAt) {
		return integrationHealth, false
	}

	if state.awaiting {
		a.scanState = "reconciling"
		return integrationBindings, true
	}

	if state.retryScan {
		a.scanState = "requesting"
		return integrationDispatch, true
	}

	if a.scanPending && (now.Sub(a.scanLast) >= scanQuiet || now.Sub(a.scanFirst) >= scanMaximumDelay) {
		state.targets = nil

		if !a.scanFull {
			for id := range a.scanChannels {
				state.targets = append(state.targets, id)
			}

			slices.Sort(state.targets)
		}

		a.scanPending, a.scanFull, a.scanChannels = false, false, nil
		a.scanState = "requesting"

		return integrationDispatch, true
	}

	return integrationHealth, false
}

func (s *scanSchedule) finished(mode integrationMode, failure error, now time.Time) {
	s.retryAt = now.Add(30 * time.Second)
	if mode == integrationDispatch {
		s.awaiting = errors.Is(failure, errScanPending)
		s.retryScan = failure != nil && !s.awaiting
	}

	if mode == integrationBindings && failure == nil {
		s.awaiting = false
	}

	if failure == nil {
		s.targets = nil
	}
}

// ScanStatus is independent of prerequisite health and retained binding errors.
type ScanStatus struct {
	State   string `json:"state"`
	Pending bool   `json:"pending"`
}

func (a *App) recordScanState(state *scanSchedule) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.scanState = "idle"
	if state.awaiting {
		a.scanState = "in_progress"
	}

	if state.retryScan {
		a.scanState = "retrying"
	}
}

func (a *App) scanStatusLocked() ScanStatus {
	state := a.scanState
	if state == "" || state == "idle" {
		state = "idle"
		if a.scanPending {
			state = "queued"
		}
	}

	return ScanStatus{State: state, Pending: a.scanPending}
}
