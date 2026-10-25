// Package diagnostics records bounded, redacted operation measurements.
package diagnostics

import (
	"sort"
	"sync"
	"time"
)

// Measurement aggregates wall time without retaining source URLs or arguments.
type Measurement struct {
	Operation string        `json:"operation"`
	Count     int64         `json:"count"`
	Total     time.Duration `json:"total_ns"`
	Maximum   time.Duration `json:"maximum_ns"`
}

// Recorder contains one aggregate per known operation.
type Recorder struct {
	mu       sync.Mutex
	values   map[string]Measurement
	counters map[string]int64
}

// Add records an operation's wall time.
func (r *Recorder) Add(operation string, elapsed time.Duration) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.values == nil {
		r.values = make(map[string]Measurement)
	}

	m, exists := r.values[operation]
	if !exists && len(r.values) >= 128 {
		return
	}

	m.Operation = operation
	m.Count++
	m.Total += elapsed
	m.Maximum = max(m.Maximum, elapsed)
	r.values[operation] = m
}

// Begin returns a closure that records the operation when called.
func (r *Recorder) Begin(operation string) func() {
	started := time.Now()
	return func() { r.Add(operation, time.Since(started)) }
}

// Snapshot returns a stable copy of the aggregate values.
func (r *Recorder) Snapshot() []Measurement {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	result := make([]Measurement, 0, len(r.values))
	for _, m := range r.values {
		result = append(result, m)
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Operation < result[j].Operation })

	return result
}

// Measure records one synchronous operation, including failed attempts.
func Measure[T any](r *Recorder, operation string, work func() (T, error)) (T, error) {
	defer r.Begin(operation)()
	return work()
}

// Run records one operation that returns only an error.
func Run(r *Recorder, operation string, work func() error) error {
	defer r.Begin(operation)()
	return work()
}
