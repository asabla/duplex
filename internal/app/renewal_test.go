package app

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/source"
)

func TestRepresentationLeaseUsesOnlySelectedTracks(t *testing.T) {
	future := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	expired := time.Now().Add(-time.Hour)
	locator := func(expiry time.Time) string {
		return "https://r1.googlevideo.com/videoplayback?" + url.Values{"expire": {strconv.FormatInt(expiry.Unix(), 10)}}.Encode()
	}
	selected := source.Candidate{Video: source.Format{ID: "1080", URL: locator(future)}, Audio: &source.Format{ID: "audio", URL: locator(future.Add(time.Hour))}}

	all := source.Metadata{Formats: []source.Format{{ID: "expired-4k", URL: locator(expired)}, selected.Video, *selected.Audio}}
	if leaseExpiry(all).After(time.Now()) {
		t.Fatal("fixture did not contain an expired unselected format")
	}

	if got := selectedLeaseExpiry(selected); !got.Equal(future.Add(-time.Minute)) {
		t.Fatal("selected representation inherited an unrelated expired locator", got)
	}
}

func TestRenewalsAreBoundedAndDoNotInvalidateUsableHandles(t *testing.T) {
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	prepared := projectionVideo(t, a, "video000001", 1)
	expires := time.Now().Add(3 * time.Minute)
	rep := mountedBytes{id: prepared.Revision, data: []byte("immutable bytes!")}
	a.cacheRepresentation(rep, expires, prepared.Metadata.ID)
	a.renewCachedSources(t.Context())

	task, err := a.Catalog.ClaimPreparationTask(t.Context())
	if err != nil || task.VideoID != prepared.Metadata.ID || task.Priority != renewalPriority {
		t.Fatal(task, err)
	}

	if cached := a.cachedRepresentation(rep.ID()); cached == nil || cached.ID() != rep.ID() {
		t.Fatal("renewal invalidated a usable representation")
	}

	if due := a.dueRenewals(time.Now()); len(due) != 0 {
		t.Fatal("renewal retried continuously", due)
	}

	if due := a.dueRenewals(time.Now().Add(16 * time.Minute)); len(due) != 1 {
		t.Fatal("renewal backoff never allowed a later attempt", due)
	}

	// Eviction must also remove renewal bookkeeping.
	for n := range 140 {
		a.cacheRepresentation(mountedBytes{id: fmt.Sprint(n), data: []byte("other")}, time.Now().Add(time.Hour), fmt.Sprint(n))
	}

	if len(a.renewAfter) > runtimeRepresentations || len(a.runtimeVideos) > runtimeRepresentations {
		t.Fatal("renewal state grew beyond the runtime cache")
	}

	if _, found := a.renewAfter[prepared.Revision]; found {
		t.Fatal("evicted representation retained renewal state")
	}
}

func TestRenewalDoesNotInterruptAnotherRenewalOrRequestedWork(t *testing.T) {
	a := &App{}

	var contexts []context.Context

	for n, priority := range []int{renewalPriority, 1000} {
		ctx, cancel := context.WithCancelCause(t.Context())
		t.Cleanup(func() { cancel(nil) })

		contexts = append(contexts, ctx)

		if !a.startPreparation(catalog.PreparationTask{VideoID: fmt.Sprint(n), Priority: priority}, cancel) {
			t.Fatal("task not registered")
		}
	}

	a.mu.Lock()
	a.interruptBackgroundBelowLocked(renewalPriority, 1)
	a.mu.Unlock()

	for _, ctx := range contexts {
		if ctx.Err() != nil {
			t.Fatal("renewal interrupted equal or higher priority work")
		}
	}
}
