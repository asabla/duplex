package app

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/config"
	"duplex/internal/plex"
)

type interruptedAssetTransport struct {
	base    http.RoundTripper
	path    string
	started chan struct{}
	stopped chan error
	blocked atomic.Bool
}

func (f *interruptedAssetTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == f.path && f.blocked.CompareAndSwap(true, false) {
		close(f.started)
		<-r.Context().Done()

		f.stopped <- context.Cause(r.Context())

		return nil, r.Context().Err()
	}

	return f.base.RoundTrip(r)
}

func TestPlaybackInterruptsOptionalAssetsAndRetainsRetry(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		path     string
		initial  bool
		activity plex.Activity
		failure  error
	}{
		{name: "thumbnail during playback", path: "/poster", activity: plex.Activity{Sessions: 1}},
		{name: "caption during analysis", path: "/captions", activity: plex.Activity{Tasks: 1}},
		{name: "initial thumbnail during scan", path: "/poster", initial: true, activity: plex.Activity{Scanning: true}},
		{name: "expired retry during monitoring failure", path: "/captions", failure: errors.New("controlled monitoring failure")},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			a, m, base := initialAssetFixture(t)
			p := config.Plex{URL: "http://plex.test:32400"}
			setPlaybackConfig(t, a, p)
			observeIdle(a, p)

			transport := &interruptedAssetTransport{base: base, path: scenario.path, started: make(chan struct{}), stopped: make(chan error, 1)}
			transport.blocked.Store(true)
			http.DefaultTransport = transport

			if !scenario.initial {
				if err := a.Catalog.QueueAsset(t.Context(), m.ID); err != nil {
					t.Fatal(err)
				}

				if _, err := a.Catalog.DB.ExecContext(t.Context(), `UPDATE asset_jobs SET state='failed',error='old failure',updated=? WHERE video_id=?`, time.Now().Add(-7*time.Hour).Unix(), m.ID); err != nil {
					t.Fatal(err)
				}
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			done := make(chan error, 1)

			finished := make(chan struct{})
			go func() {
				defer close(finished)

				if scenario.initial {
					done <- a.queueRecordingAssets(ctx, m, true)
				} else {
					done <- a.runAssetWork(ctx, m.ID)
				}
			}()

			t.Cleanup(func() {
				cancel()

				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Error("asset request did not stop")
				}
			})

			select {
			case <-transport.started:
			case <-time.After(3 * time.Second):
				t.Fatal("asset request did not start")
			}

			a.observePlayback(p, scenario.activity, scenario.failure, time.Now())

			select {
			case cause := <-transport.stopped:
				if !errors.Is(cause, errBackgroundDeferred) {
					t.Fatal("asset request did not yield to playback protection", cause)
				}
			case <-time.After(time.Second):
				t.Fatal("playback left an optional request running")
			}

			if err := <-done; err != nil {
				t.Fatal("playback deferral became an asset failure", err)
			}

			var state, message string

			var updated int64
			if err := a.Catalog.DB.QueryRowContext(t.Context(), `SELECT state,error,updated FROM asset_jobs WHERE video_id=?`, m.ID).Scan(&state, &message, &updated); err != nil || state != "queued" || message != "" || updated != 0 {
				t.Fatal("interrupted asset was not ready for retry", state, message, updated, err)
			}

			retained := a.loadAssets(m.ID)
			observeIdle(a, p)

			if id, err := a.Catalog.NextAsset(t.Context()); err != nil || id != m.ID {
				t.Fatal("asset retry was lost", id, err)
			}

			if err := a.runAssetWork(t.Context(), m.ID); err != nil {
				t.Fatal(err)
			}

			assets := a.loadAssets(m.ID)
			if len(assets) != 2 {
				t.Fatal("resumed work did not complete artwork and captions", assets)
			}

			if scenario.path == "/captions" && (len(retained) != 1 || retained[0].Media.ID() != assets[1].Media.ID()) {
				t.Fatal("completed thumbnail changed during deferral", retained, assets)
			}
		})
	}
}

func TestAssetAdmissionRechecksPlaybackAfterCapacityWait(t *testing.T) {
	p := config.Plex{URL: "http://plex.test:32400"}
	a := playbackFixture(t, p)
	observeIdle(a, p)

	release, err := a.acquireAsset(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)

	go func() {
		work, finish, failure := a.startAssetWork(ctx)
		if failure == nil {
			failure = context.Cause(work)

			finish()
		}

		done <- failure
	}()

	a.observePlayback(p, plex.Activity{Sessions: 1}, nil, time.Now())
	release()

	if err := <-done; !errors.Is(err, errBackgroundDeferred) {
		t.Fatal("capacity wait admitted optional work after playback started", err)
	}

	// The old cancellation must not cancel a later operation after recovery.
	observeIdle(a, p)

	work, finish, err := a.startAssetWork(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()

	if work.Err() != nil {
		t.Fatal("old cancellation leaked into the next asset operation", work.Err())
	}
}

func TestAssetResolutionDeferralKeepsForegroundWaiter(t *testing.T) {
	a, adapter := sharedMetadataFixture(t)
	setCaptionFetching(t, a, true)

	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}

	id := "video000000"
	a.forgetMetadata(id)

	if err := a.Catalog.QueueAsset(t.Context(), id); err != nil {
		t.Fatal(err)
	}

	p := config.Plex{URL: "http://plex.test:32400"}
	setPlaybackConfig(t, a, p)
	observeIdle(a, p)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	assets := make(chan error, 1)
	go func() { assets <- a.runAssetWork(ctx, id) }()

	select {
	case <-adapter.started:
	case <-ctx.Done():
		t.Fatal("asset resolution did not start")
	}

	foreground := make(chan error, 1)

	go func() { _, err := a.playbackMetadata(ctx, id, false); foreground <- err }()

	waitSharedWaiters(t, &a.resolutionWork, id, 2)
	a.observePlayback(p, plex.Activity{Sessions: 1}, nil, time.Now())

	if err := <-assets; err != nil {
		t.Fatal("deferred assets failed", err)
	}

	waitSharedWaiters(t, &a.resolutionWork, id, 1)

	select {
	case err := <-foreground:
		t.Fatal("asset cancellation stopped the foreground source request", err)
	default:
	}

	close(adapter.release)

	if err := <-foreground; err != nil || adapter.calls.Load() != 1 {
		t.Fatal("foreground playback lost its shared resolution", err, adapter.calls.Load())
	}
}
