package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"duplex/internal/config"
	"duplex/internal/diagnostics"
)

type metricsTransportFixture struct {
	body    io.ReadCloser
	failure error
	called  bool
}

func (f *metricsTransportFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	f.called = true
	if r.Header.Get("Range") != "bytes=0-2" {
		return nil, errors.New("range header changed")
	}
	if f.failure != nil {
		return nil, f.failure
	}
	return &http.Response{StatusCode: 200, Body: f.body, Header: make(http.Header), Request: r}, nil
}

type metricsBodyFixture struct {
	io.Reader
	closed  bool
	failure error
}

func (f *metricsBodyFixture) Close() error { f.closed = true; return f.failure }

func TestMediaMetricsPreserveRequestReadAndCloseResults(t *testing.T) {
	closeFailure := errors.New("controlled close failure")
	body := &metricsBodyFixture{Reader: bytes.NewReader([]byte("abc")), failure: closeFailure}
	transport := &metricsTransportFixture{body: body}
	original := &http.Client{Timeout: 7 * time.Second, Transport: transport}
	a := &App{Measurements: &diagnostics.Recorder{}, httpClient: original}
	client := a.mediaClient()
	if client == original || client.Timeout != original.Timeout || original.Transport != transport {
		t.Fatal("metrics changed the shared client")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://r1.googlevideo.com/media?private=not-in-diagnostics", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=0-2")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil || string(data) != "abc" {
		t.Fatal(string(data), err)
	}
	if err = response.Body.Close(); !errors.Is(err, closeFailure) || !body.closed {
		t.Fatal("body close result was lost", err)
	}
	transport.failure = context.Canceled
	failed, err := client.Do(request)
	if failed != nil {
		if closeErr := failed.Body.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("transport failure was lost", err)
	}
	counters := a.Measurements.Counters()
	if len(counters) != 2 || counters[0].Value != 2 || counters[1].Value != 3 {
		t.Fatal(counters)
	}
	encoded, err := json.Marshal(counters)
	if err != nil || bytes.Contains(encoded, []byte("private")) || bytes.Contains(encoded, []byte("googlevideo")) {
		t.Fatal("diagnostics retained request data", err)
	}
	unconfigured := (&App{}).mediaClient()
	if unconfigured.Transport == nil || unconfigured.Timeout != 30*time.Second {
		t.Fatal("default client contract changed")
	}
}

func TestQueueWaitUsesTheDurableClaimTimestamp(t *testing.T) {
	a := pipelineApp(t, pipelineChannel(config.DefaultSelection()), fixtureCollection(1))
	a.Measurements = &diagnostics.Recorder{}
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	queued := time.Now().Add(-2 * time.Second).Unix()
	if _, err := a.Catalog.DB.ExecContext(t.Context(), `UPDATE preparation_jobs SET updated=?`, queued); err != nil {
		t.Fatal(err)
	}
	task, err := a.Catalog.ClaimPreparationTask(t.Context())
	if err != nil || task.QueuedAt != queued {
		t.Fatal(task, err)
	}
	a.runPreparation(t.Context(), task)
	found := false
	for _, measurement := range a.Measurements.Snapshot() {
		if measurement.Operation == "preparation.queue" {
			found = true
			if measurement.Count != 1 || measurement.Total < 2*time.Second {
				t.Fatal(measurement)
			}
		}
	}
	if !found {
		t.Fatal("durable queue wait was not measured")
	}
}

func TestMediaCountersMatchTheControlledOrigin(t *testing.T) {
	a, _, origin := mediaPipeline(t)
	if err := a.Sync(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	if err := a.prepareRecording(t.Context(), "video000000", false); err != nil {
		t.Fatal(err)
	}
	counters := a.Measurements.Counters()
	if len(counters) != 2 || counters[0].Value != origin.requests.Load() || counters[1].Value != origin.bytes.Load() || counters[1].Value <= 0 {
		t.Fatal(counters, origin.requests.Load(), origin.bytes.Load())
	}
}
