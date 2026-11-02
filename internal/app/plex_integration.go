package app

import (
	"context"
	"errors"
	"os"
	"time"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/plex"
)

// libraryState serializes Plex updates and bounded hint publication.
// App.mu protects bookkeeping; the slot channels serialize external operations.
type libraryState struct {
	hintWriters    chan struct{}
	libraryUpdates chan struct{}
	hints          map[string]cachedHints
	scanWake       chan struct{}
	scanPending    bool
	scanState      string
	scanFirst      time.Time
	scanLast       time.Time
	scanChannels   map[string]bool
	scanFull       bool
}

func (a *App) plexClientFor(p config.Plex) (*plex.Client, error) {
	if p.URL == "" {
		return nil, errors.New("plex connection is not configured")
	}

	token := ""

	if p.TokenFile != "" {
		b, e := os.ReadFile(p.TokenFile)
		if e != nil {
			return nil, errors.New("cannot read Plex token file")
		}

		token = string(b)
	}

	client, err := plex.New(p.URL, token)
	if err == nil {
		client.Count = a.Measurements.Count
	}

	return client, err
}

// Scan checks the mount and Plex prerequisites before requesting library discovery.
func (a *App) Scan(ctx context.Context) error {
	defer a.Measurements.Begin("plex.scan")()
	return a.integration(ctx, integrationScan)
}

type integrationMode int

const (
	integrationHealth integrationMode = iota
	integrationScan
	integrationBindings
	integrationDispatch
)

func (a *App) integration(ctx context.Context, mode integrationMode) error {
	return a.integrationTargets(ctx, mode, nil)
}

func (a *App) integrationTargets(ctx context.Context, mode integrationMode, targets []string) (err error) {
	if a.mount == nil {
		return nil
	}

	if !a.playbackConfig().UpdatesEnabled() {
		a.mu.Lock()
		a.plexError, a.plexBindingError = "", ""
		a.mu.Unlock()

		return nil
	}

	if mode != integrationHealth {
		release, failure := a.admitPlexUpdate(ctx)
		if failure != nil {
			if mode == integrationScan {
				a.requestScan()
			}

			return failure
		}
		defer release()
	}

	defer func() {
		if mode == integrationScan && errors.Is(err, errBackgroundDeferred) {
			a.requestScan()
		}

		a.integrationResult(mode, err)
	}()

	return a.runIntegration(ctx, mode, targets)
}

func (a *App) runIntegration(ctx context.Context, mode integrationMode, targets []string) error {
	if !a.mount.Ready() {
		a.mu.Lock()
		a.mountError = "Library mount is unavailable; scans are disabled"
		a.mu.Unlock()

		return errors.New("library mount is unavailable")
	}

	connection := a.Config.Snapshot().Config.Plex

	client, err := a.plexClientFor(connection)
	if err != nil {
		return err
	}

	if _, err = client.Check(ctx); err != nil {
		return err
	}

	if err = client.LibraryHealth(ctx, connection.Section); err != nil {
		return err
	}

	if mode == integrationHealth {
		return nil
	}

	release, err := a.acquireLibraryUpdate(ctx)
	if err != nil {
		return err
	}
	defer release()

	return a.updatePlexLibrary(ctx, client, connection, mode, targets)
}

func (a *App) updatePlexLibrary(ctx context.Context, client *plex.Client, connection config.Plex, mode integrationMode, targets []string) error {
	if mode == integrationScan || mode == integrationDispatch {
		if err := a.scanPlex(ctx, client, connection, targets); err != nil {
			return err
		}

		if mode == integrationDispatch {
			return errScanPending
		}
	}

	if mode == integrationBindings {
		busy, err := client.Scanning(ctx, connection.Section)
		if err != nil {
			return err
		}

		if busy {
			return errScanPending
		}
	} else if err := client.WaitScan(ctx, connection.Section); err != nil {
		return err
	}

	return a.bindPlex(ctx, client, connection)
}

func (a *App) bindPlex(ctx context.Context, client *plex.Client, connection config.Plex) (failure error) {
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()

		a.plexBindingError = ""
		if failure != nil && !errors.Is(failure, errBackgroundDeferred) {
			a.plexBindingError = failure.Error()
		}
	}()

	run := time.Now().UnixNano()

	if err := client.WalkBindings(ctx, connection.Section, func(bindings []plex.Binding) error {
		observations := make([]catalog.ObservedEpisode, 0, len(bindings))
		for _, binding := range bindings {
			if occurrence := plex.OccurrenceID(binding.GUID); occurrence != "" {
				observations = append(observations, catalog.ObservedEpisode{Occurrence: occurrence, Key: binding.Key, GUID: binding.GUID, Index: binding.Index})
			}
		}

		return a.Catalog.ObserveEpisodes(ctx, connection.URL, run, observations)
	}); err != nil {
		return err
	}

	for _, promote := range []bool{false, true} {
		if err := a.applyPlexOrder(ctx, client, connection, run, promote); err != nil {
			return err
		}
	}

	return nil
}

func (a *App) applyPlexOrder(ctx context.Context, client *plex.Client, connection config.Plex, run int64, promote bool) error {
	after := ""
	for {
		orders, err := a.Catalog.EpisodeOrderPage(ctx, visibility(a.Config.Snapshot().Config), connection.URL, run, promote, after, 128)
		if err != nil {
			return err
		}

		for _, order := range orders {
			if err = a.applyEpisodeOrder(ctx, client, connection, run, promote, order); err != nil {
				return err
			}

			after = order.Occurrence
		}

		if len(orders) < 128 {
			return nil
		}
	}
}

func (a *App) setPlexError(e error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.plexError = ""
	if e != nil {
		a.plexError = e.Error()
	}
}

func (a *App) integrationResult(_ integrationMode, err error) {
	if errors.Is(err, errBackgroundDeferred) {
		return
	}

	if errors.Is(err, errScanPending) {
		err = nil
	}

	a.setPlexError(err)
}

func (a *App) scanPlex(ctx context.Context, client *plex.Client, connection config.Plex, targets []string) (err error) {
	busy, err := client.Scanning(ctx, connection.Section)
	if err != nil {
		return err
	}

	if busy {
		return errBackgroundDeferred
	}

	if err := a.prepareLibraryHints(ctx); err != nil {
		return err
	}

	if err := a.bindPlex(ctx, client, connection); err != nil {
		return err
	}

	if !a.automaticUpdatesAllowed() {
		return errBackgroundDeferred
	}

	err = client.ScanChannels(ctx, connection.Section, targets)
	if errors.Is(err, plex.ErrRequestUncertain) {
		// Reconcile first, then retain one paced retry for an unacknowledged request.
		a.requestScan()
		return errScanPending
	}

	return err
}

func (a *App) applyEpisodeOrder(ctx context.Context, client *plex.Client, connection config.Plex, run int64, promote bool, order catalog.EpisodeOrder) error {
	if !a.automaticUpdatesAllowed() {
		return errBackgroundDeferred
	}

	if order.Temporary <= 1<<30 || order.Temporary >= 1<<31 || order.Desired < 0 || order.Desired >= 1<<30 {
		return errors.New("playlist exceeds the Plex episode index range")
	}

	index := order.Temporary
	if promote {
		index = order.Desired
	}

	if err := client.SetEpisodeOrder(ctx, connection.Section, order.Key, index); err != nil {
		return err
	}

	return a.Catalog.AppliedEpisodeOrder(ctx, connection.URL, run, order.Occurrence, index)
}
