package diagnostics

import (
	"math"
	"sort"
)

// Counter records a cumulative quantity without source identities or locators.
type Counter struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// Count adds a positive quantity. Cardinality and integer growth are bounded.
func (r *Recorder) Count(name string, value int64) {
	if r == nil || value <= 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.counters == nil {
		r.counters = make(map[string]int64)
	}

	current, exists := r.counters[name]
	if !exists && len(r.counters) >= 128 {
		return
	}

	if value > math.MaxInt64-current {
		value = math.MaxInt64 - current
	}

	r.counters[name] = current + value
}

// Counters returns a stable copy of cumulative quantities for diagnostics.
func (r *Recorder) Counters() []Counter {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Counter, 0, len(r.counters))
	for name, value := range r.counters {
		out = append(out, Counter{Name: name, Value: value})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}
