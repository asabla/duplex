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
		if err = config.AtomicWrite(filepath.Join(directory, kind+"-measurements.json"), b, 0o600); err != nil {
			return err
		}
		counters := application.Measurements.Counters()
		if len(counters) == 0 {
			return nil
		}
		encoded, err := json.MarshalIndent(counters, "", "  ")
		if err != nil {
			return err
		}
		return config.AtomicWrite(filepath.Join(directory, kind+"-counters.json"), encoded, 0o600)
	})
}
