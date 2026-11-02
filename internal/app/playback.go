package app

import (
	"context"
	"time"

	"duplex/internal/config"
	"duplex/internal/diagnostics"
	"duplex/internal/plex"
)

const (
	playbackPoll      = 2 * time.Second
	playbackFreshness = 5 * time.Second
	playbackIdleGrace = 30 * time.Second
)

// PlaybackStatus reports admission of background work without exposing sessions.
type PlaybackStatus struct {
	State            string    `json:"state"`
	Reason           string    `json:"reason"`
	BackgroundPaused bool      `json:"background_paused"`
	Sessions         int       `json:"sessions"`
	ScannerBusy      bool      `json:"scanner_busy"`
	LastCheck        time.Time `json:"last_check"`
	LastSuccess      time.Time `json:"last_success"`
	ResumeAfter      time.Time `json:"resume_after"`
}

// App.mu protects observations, admission channels, and policy generations.
type playbackState struct {
	observedPolicy        playbackPolicy
	playback              PlaybackStatus
	idleSince             time.Time
	playbackWake          chan struct{}
	backgroundMetadata    chan struct{}
	backgroundPreparation chan struct{}
	backgroundHeavy       chan struct{}
}

// Compare values, not optional-field pointers from cloned configuration snapshots.
type playbackPolicy struct {
	url, tokenFile, section string
	protect                 bool
}

func policyFor(p config.Plex) playbackPolicy {
	return playbackPolicy{p.URL, p.TokenFile, p.Section, p.ProtectPlayback()}
}

func (a *App) playbackConfig() config.Plex {
	if a.Config == nil {
		return config.Plex{}
	}

	return a.Config.Snapshot().Config.Plex
}

// PlaybackStatus evaluates freshness on every read; a stalled monitor cannot
// leave background work admitted using an old idle response.
func (a *App) PlaybackStatus() PlaybackStatus {
	p := a.playbackConfig()

	a.mu.RLock()
	defer a.mu.RUnlock()

	return a.playbackStatusLocked(p, time.Now())
}

func (a *App) playbackStatusLocked(p config.Plex, now time.Time) PlaybackStatus {
	if p.URL == "" {
		return PlaybackStatus{State: "unconfigured", Reason: "Configure Plex to protect playback in other libraries. Background work is limited."}
	}

	if !p.ProtectPlayback() {
		return PlaybackStatus{State: "disabled", Reason: "Plex playback protection is disabled."}
	}

	s := a.playback
	if a.observedPolicy != policyFor(p) || s.LastCheck.IsZero() {
		return PlaybackStatus{State: "checking", Reason: "Waiting for Plex playback status.", BackgroundPaused: true}
	}

	if now.Sub(s.LastCheck) > playbackFreshness {
		s.State, s.Reason, s.BackgroundPaused = "unavailable", "Plex playback status is unavailable; background work is paused.", true
	}

	if s.State == "idle" && now.Before(s.ResumeAfter) {
		s.BackgroundPaused = true
		s.Reason = "Waiting for the idle grace period before resuming background work."
	}

	return s
}

func (a *App) observePlayback(p config.Plex, activity plex.Activity, failure error, now time.Time) {
	if policyFor(p) != policyFor(a.playbackConfig()) {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.observedPolicy != policyFor(p) {
		a.playback, a.idleSince = PlaybackStatus{}, time.Time{}
	}

	a.observedPolicy = policyFor(p)
	a.updatePlaybackLocked(activity, failure, now)
	a.signalPlaybackLocked()

	if a.playbackStatusLocked(p, now).BackgroundPaused {
		for _, run := range a.preparations {
			if run.priority < streamRenewalPriority && !run.interrupted {
				run.interrupted = true
				run.cancel(errPlaybackPriority)
			}
		}
	}
}

func (a *App) updatePlaybackLocked(activity plex.Activity, failure error, now time.Time) {
	if now.Sub(a.playback.LastCheck) > playbackFreshness {
		a.idleSince = time.Time{}
	}

	s := PlaybackStatus{LastCheck: now, LastSuccess: a.playback.LastSuccess, BackgroundPaused: true}
	s.State, s.Reason = "unavailable", "Plex playback status is unavailable; background work is paused."

	if failure == nil {
		s.LastSuccess, s.Sessions, s.ScannerBusy = now, activity.Sessions, activity.Scanning

		s.State, s.Reason = "active", "Plex has active sessions; background work is paused."
		if activity.Sessions == 0 {
			s.State, s.Reason = "scanning", "Plex is scanning the dedicated library; background work is paused."

			if !activity.Scanning {
				if a.idleSince.IsZero() {
					a.idleSince = now
				}

				s.State, s.Reason, s.BackgroundPaused = "idle", "Plex is idle; background work can run.", false
				s.ResumeAfter = a.idleSince.Add(playbackIdleGrace)
			}
		}
	}

	if s.State != "idle" {
		a.idleSince = time.Time{}
	}

	a.playback = s
}

func (a *App) signalPlaybackLocked() {
	if a.playbackWake != nil {
		close(a.playbackWake)
	}

	a.playbackWake = make(chan struct{})
}

func (a *App) playbackWorker(ctx context.Context) {
	defer a.wg.Done()

	ticker := time.NewTicker(playbackPoll)
	defer ticker.Stop()

	for {
		a.checkPlayback(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) checkPlayback(ctx context.Context) {
	p := a.playbackConfig()
	if !p.ProtectPlayback() {
		return
	}

	work, cancel := context.WithTimeout(ctx, playbackPoll)
	defer cancel()

	activity, err := diagnostics.Measure(a.Measurements, "plex.activity", func() (plex.Activity, error) {
		client, failure := a.plexClientFor(p)
		if failure != nil {
			return plex.Activity{}, failure
		}

		return client.Activity(work, p.Section)
	})

	a.observePlayback(p, activity, err, time.Now())
	a.wakePreparation()
}
