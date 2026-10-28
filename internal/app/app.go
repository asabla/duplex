// Package app contains services shared by the JSON and HTMX handlers.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"duplex/internal/cache"
	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/diagnostics"
	"duplex/internal/source"
)

// App coordinates catalogue discovery, media preparation, publication, and integration.
type App struct {
	schedulerState
	preparationState
	representationState
	libraryState
	publicationState
	assetState

	httpClient       *http.Client
	Measurements     *diagnostics.Recorder
	Config           *config.Manager
	Catalog          *catalog.Store
	Cache            *cache.Store
	Source           source.SourceAdapter
	mount            libraryRuntime
	mu               sync.RWMutex // Retained across state groups to preserve cross-operation atomicity.
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	original         config.Config
	toolError        string
	mountError       string
	plexError        string
	plexBindingError string
	restart          bool
	cacheError       string
}

type libraryRuntime interface {
	Ready() bool
	Done() <-chan struct{}
	Close() error
}

func (a *App) operationFailure(operation string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}

	// Do not log error strings: process and transport errors can contain locators.
	slog.Error("duplex operation failed", "operation", operation)
	a.Measurements.Add("failure."+operation, 0)
}
