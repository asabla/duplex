package app

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

func BenchmarkMetadataUnderWrites(b *testing.B) {
	for _, operation := range []metadataOperation{metadataOperations(true)[0], metadataOperations(true)[3], metadataOperations(true)[4]} {
		b.Run(operation.name, func(b *testing.B) {
			a := benchmarkMetadataCatalogue(b, 10000, true)
			provider := catalogueFS{app: a}
			runMetadataWriteWorkload(b, a, func(ctx context.Context) error {
				_, err := provider.Stat(ctx, operation.path)
				return err
			})
		})
	}
}

// Offered requests keep their planned start times so a blocked reader does not
// silently remove arrivals from the latency distribution.
func runMetadataWriteWorkload(b *testing.B, a *App, stat func(context.Context) error) {
	b.Helper()

	if err := stat(b.Context()); err != nil {
		b.Fatal(err)
	}

	iterations := 0
	for b.Loop() {
		if iterations > 0 {
			b.Fatal("use -benchtime=1x for the fixed writer workload")
		}

		iterations++
		ctx, cancel := context.WithTimeout(b.Context(), 8*time.Second)
		start := time.Now().Add(50 * time.Millisecond)
		results := make(chan [2][]int64, 4)
		failures := make(chan error, 5)

		var workers sync.WaitGroup

		workers.Go(func() {
			for i := range 10 {
				if err := waitBenchmarkArrival(ctx, start.Add(time.Duration(i)*100*time.Millisecond)); err != nil {
					failures <- err
					return
				}

				_, err := a.Catalog.DB.ExecContext(ctx, `UPDATE playlist_members SET updated=updated+1 WHERE playlist_id='PLmetadata' AND file_episode<=1000`)
				if err != nil {
					failures <- err
					return
				}
			}
		})

		for range 4 {
			workers.Go(func() {
				scheduled := make([]int64, 0, 200)
				service := make([]int64, 0, 200)

				for i := range 200 {
					due := start.Add(time.Duration(i) * 5 * time.Millisecond)
					if err := waitBenchmarkArrival(ctx, due); err != nil {
						failures <- err
						return
					}

					began := time.Now()

					if err := stat(ctx); err != nil {
						failures <- err
						return
					}

					finished := time.Now()
					service = append(service, finished.Sub(began).Nanoseconds())
					scheduled = append(scheduled, finished.Sub(due).Nanoseconds())
				}

				results <- [2][]int64{scheduled, service}
			})
		}

		workers.Wait()
		cancel()
		close(failures)
		close(results)

		for err := range failures {
			b.Fatal(err)
		}

		scheduled, service := []int64{}, []int64{}
		for result := range results {
			scheduled = append(scheduled, result[0]...)
			service = append(service, result[1]...)
		}

		if len(scheduled) != 800 {
			b.Fatal("offered requests were lost", len(scheduled))
		}

		slices.Sort(scheduled)
		slices.Sort(service)
		b.ReportMetric(float64(scheduled[759]), "p95-scheduled-ns")
		b.ReportMetric(float64(scheduled[791]), "p99-scheduled-ns")
		b.ReportMetric(float64(service[759]), "p95-service-ns")
		b.ReportMetric(float64(service[791]), "p99-service-ns")
	}
}

func waitBenchmarkArrival(ctx context.Context, due time.Time) error {
	delay := time.Until(due)
	if delay <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
