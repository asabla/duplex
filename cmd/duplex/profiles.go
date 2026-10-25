package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"duplex/internal/app"
	"duplex/internal/config"
	"duplex/internal/diagnostics"
)

func startProfile(ctx context.Context, application *app.App, directory, kind string, duration time.Duration) (*diagnostics.Capture, error) {
	if directory == "" {
		return nil, nil
	}
	return diagnostics.StartCapture(ctx, directory, kind, duration, func() error {
		b, err := json.MarshalIndent(application.Measurements.Snapshot(), "", "  ")
		if err != nil {
			return err
		}
		return config.AtomicWrite(filepath.Join(directory, kind+"-measurements.json"), b, 0o600)
	})
}
