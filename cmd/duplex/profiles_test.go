package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"duplex/internal/app"
	"duplex/internal/diagnostics"
)

func TestPrivateProfileRetainsRedactedCounters(t *testing.T) {
	recorder := &diagnostics.Recorder{}
	recorder.Count("media.http_requests", 3)
	recorder.Count("media.upstream_bytes", 900)

	directory := t.TempDir()

	capture, err := startProfile(t.Context(), &app.App{Measurements: recorder}, directory, "allocs", time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if err = capture.Stop(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(directory, "allocs-counters.json")

	data, err := os.ReadFile(path) //nolint:gosec // G304: This reads only the generated private profile beneath t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	var counters []diagnostics.Counter
	if err = json.Unmarshal(data, &counters); err != nil || len(counters) != 2 || counters[0].Value != 3 || counters[1].Value != 900 {
		t.Fatal(counters, err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(info, err)
	}
}
