package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/source"
)

type blockedPreparationSource struct {
	*collectionSource
	started   chan string
	cancelled chan string
}

func (s *blockedPreparationSource) Resolve(ctx context.Context, id string) (source.Metadata, error) {
	select {
	case s.started <- id:
	case <-ctx.Done():
		return source.Metadata{}, ctx.Err()
	}

	<-ctx.Done()

	select {
	case s.cancelled <- id:
	default:
	}

	return source.Metadata{}, ctx.Err()
}

func nextPreparationEvent(t *testing.T, events <-chan string) string {
	t.Helper()

	select {
	case value := <-events:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("preparation event did not arrive")
		return ""
	}
}

func TestRequestedPreparationInterruptsBackfillAndResumesAfterRestart(t *testing.T) {
	adapter := &blockedPreparationSource{collectionSource: fixtureCollection(3), started: make(chan string, 4), cancelled: make(chan string, 4)}

	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "latest", Limit: 3}), adapter)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { a.cancel(); a.wg.Wait() })

	for range 2 {
		a.wg.Add(1)
		go a.preparationWorker(a.ctx)
	}

	first, second := nextPreparationEvent(t, adapter.started), nextPreparationEvent(t, adapter.started)
	if first == second || first == "video000002" || second == "video000002" {
		t.Fatal(first, second)
	}

	if err := a.PrepareNow(t.Context(), "video000002"); err != nil {
		t.Fatal(err)
	}

	cancelled := nextPreparationEvent(t, adapter.cancelled)
	if cancelled != "video000001" {
		t.Fatal("highest-priority background item was interrupted", cancelled)
	}

	if requested := nextPreparationEvent(t, adapter.started); requested != "video000002" {
		t.Fatal("requested recording did not take the freed worker", requested)
	}

	paused, err := a.Catalog.Video(t.Context(), cancelled)
	if err != nil || paused.Preparation != "queued" || paused.PreparationError != "" {
		t.Fatal("interrupted backfill was not checkpointed", paused, err)
	}

	// Shutdown leaves active work resumable. It must not convert cancellation into
	// a permanent source failure.
	a.cancel()
	a.wg.Wait()

	if err = a.Catalog.ResetPreparations(t.Context(), visibility(a.Config.Snapshot().Config)); err != nil {
		t.Fatal(err)
	}

	requested, err := a.Catalog.ClaimPreparation(t.Context())
	if err != nil || requested != "video000002" {
		t.Fatal("requested work did not retain restart priority", requested, err)
	}
}

func TestForegroundBindingsReserveBothPreparationSlots(t *testing.T) {
	a := &App{}
	contexts := make([]context.Context, 2)

	for n := range 2 {
		ctx, cancel := context.WithCancelCause(t.Context())
		t.Cleanup(func() { cancel(nil) })

		contexts[n] = ctx
		if !a.startPreparation(catalog.PreparationTask{VideoID: string(rune('a' + n)), Priority: n}, cancel) {
			t.Fatal("background task was not registered")
		}
	}

	first := a.beginForeground()

	if !errors.Is(context.Cause(contexts[0]), errPlaybackPriority) || contexts[1].Err() != nil {
		t.Fatal("one requested bind did not retain one background slot")
	}

	second := a.beginForeground()

	if !errors.Is(context.Cause(contexts[1]), errPlaybackPriority) {
		t.Fatal("second requested bind did not reserve capacity")
	}

	if !a.backgroundPaused() {
		t.Fatal("backfill was not paused")
	}

	first()
	second()

	if a.backgroundPaused() {
		t.Fatal("completed requests left backfill paused")
	}
}
