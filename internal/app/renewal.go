package app

import (
	"context"
	"time"

	"duplex/internal/source"
)

const renewalPriority = 200

func selectedLeaseExpiry(candidate source.Candidate) time.Time {
	formats := []source.Format{candidate.Video}
	if candidate.Audio != nil {
		formats = append(formats, *candidate.Audio)
	}

	return leaseExpiry(source.Metadata{Formats: formats})
}

// renewCachedSources considers only the bounded runtime cache. It retains usable
// handles while new source leases are verified, and limits failed renewals.
func (a *App) renewCachedSources(ctx context.Context) {
	due := a.dueRenewals(time.Now())

	visible := visibility(a.Config.Snapshot().Config)
	for _, id := range due {
		selected, err := a.Catalog.IsSelected(ctx, id, visible)
		if err != nil {
			a.operationFailure("source renewal selection", err)
			continue
		}

		if !selected {
			continue
		}

		if err = a.Catalog.QueuePreparation(ctx, id, renewalPriority); err != nil {
			a.operationFailure("source renewal queue", err)
			continue
		}

		a.mu.Lock()
		a.interruptBackgroundBelowLocked(renewalPriority, 1)
		a.mu.Unlock()
		a.wakePreparation()
	}
}

func (a *App) dueRenewals(now time.Time) []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.renewAfter == nil {
		a.renewAfter = map[string]time.Time{}
	}

	due := []string{}

	for revision := range a.reps {
		id := a.runtimeVideos[revision]

		expiry := a.leases[revision]
		if id == "" || expiry.IsZero() || expiry.After(now.Add(5*time.Minute)) || now.Before(a.renewAfter[revision]) {
			continue
		}

		a.renewAfter[revision] = now.Add(15 * time.Minute)

		due = append(due, id)
	}

	return due
}
