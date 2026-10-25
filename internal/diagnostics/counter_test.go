package diagnostics

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

func TestConcurrentCountersAreStableBoundedAndSaturating(t *testing.T) {
	var absent *Recorder
	absent.Count("nil", 1)
	if absent.Counters() != nil {
		t.Fatal("nil recorder retained counters")
	}
	r := &Recorder{}
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 500 {
				r.Count("media.http_requests", 1)
				r.Count("media.upstream_bytes", 3)
			}
		}()
	}
	workers.Wait()
	snapshot := r.Counters()
	if len(snapshot) != 2 || snapshot[0].Name != "media.http_requests" || snapshot[0].Value != 2000 || snapshot[1].Value != 6000 {
		t.Fatal(snapshot)
	}
	snapshot[0].Value = 0
	r.Count("ignored", 0)
	r.Count("ignored", -1)
	r.Count("large", math.MaxInt64)
	r.Count("large", 100)
	for i := range 200 {
		r.Count(fmt.Sprintf("counter.%03d", i), 1)
		r.Add(fmt.Sprintf("operation.%03d", i), time.Second)
	}
	r.Count("media.http_requests", 1)
	counters := r.Counters()
	if len(counters) != 128 || len(r.Snapshot()) != 128 {
		t.Fatal("diagnostic cardinality grew without a bound", len(counters), len(r.Snapshot()))
	}
	for _, counter := range counters {
		if counter.Name == "large" && counter.Value != math.MaxInt64 {
			t.Fatal("counter overflowed", counter)
		}
		if counter.Name == "media.http_requests" && counter.Value != 2001 {
			t.Fatal("existing counter stopped at the cardinality bound", counter)
		}
	}
	r.Add("operation.000", time.Second)
	if first := r.Snapshot()[0]; first.Count != 2 || first.Total != 2*time.Second {
		t.Fatal(first)
	}
}
