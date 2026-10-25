package app

import (
	"context"
	"errors"
	"time"

	"duplex/internal/catalog"
)

// preparationState shares capacity between foreground bindings and background jobs.
// App.mu protects its counters, run map, and channel initialization.
type preparationState struct {
	prepareSlots chan struct{}
	preparations map[string]*preparationRun
	foreground   int
	prepareWake  chan struct{}
}

var errPlaybackPriority = errors.New("background preparation yielded to requested playback")

type preparationRun struct {
	priority    int
	started     time.Time
	cancel      context.CancelCauseFunc
	interrupted bool
}

func (a *App) backgroundPaused() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.foreground > 0
}

func (a *App) beginForeground() func() {
	a.mu.Lock()
	a.foreground++
	a.interruptBackgroundLocked(max(0, 2-a.foreground))
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		a.foreground--
		a.mu.Unlock()
		a.wakePreparation()
	}
}

func (a *App) startPreparation(task catalog.PreparationTask, cancel context.CancelCauseFunc) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if task.Priority < 1000 && a.foreground > 0 {
		return false
	}
	if a.preparations == nil {
		a.preparations = map[string]*preparationRun{}
	}
	a.preparations[task.VideoID] = &preparationRun{priority: task.Priority, started: time.Now(), cancel: cancel}
	return true
}

func (a *App) endPreparation(id string) {
	a.mu.Lock()
	delete(a.preparations, id)
	a.mu.Unlock()
}

// interruptBackgroundLocked leaves capacity for requested work. Callers hold a.mu.
func (a *App) interruptBackgroundLocked(keep int) { a.interruptBackgroundBelowLocked(1000, keep) }

func (a *App) interruptBackgroundBelowLocked(priority, keep int) {
	for {
		count := 0
		var candidate *preparationRun
		for _, task := range a.preparations {
			if task.priority >= priority || task.interrupted {
				continue
			}
			count++
			if candidate == nil || task.priority < candidate.priority || (task.priority == candidate.priority && task.started.Before(candidate.started)) {
				candidate = task
			}
		}
		if count <= keep {
			return
		}
		candidate.interrupted = true
		candidate.cancel(errPlaybackPriority)
	}
}
