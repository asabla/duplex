package app

import (
	"testing"

	"duplex/internal/config"
)

// Compare the previous upload query with durable rule checkpoints. Both use
// the same 10,000 staged recordings and unchanged membership after warm-up.
func BenchmarkSharedSelectionReconcile(b *testing.B) {
	for _, mode := range []string{"all", "latest"} {
		for _, previous := range []bool{true, false} {
			name := mode + "/checkpointed"
			if previous {
				name = mode + "/previous"
			}

			b.Run(name, func(b *testing.B) {
				a := benchmarkCatalogue(b, 10000)
				if _, err := a.Catalog.DB.ExecContext(b.Context(), `UPDATE videos SET published=1700000000+number`); err != nil {
					b.Fatal(err)
				}

				run, err := a.Catalog.StartJob(b.Context(), testOwner)
				if err != nil {
					b.Fatal(err)
				}

				if _, err = a.Catalog.DB.ExecContext(b.Context(), `INSERT INTO discovery_items(run_id,collection,video_id,position) SELECT ?,'uploads',id,number FROM videos`, run); err != nil {
					b.Fatal(err)
				}

				limit := 0
				if mode == "latest" {
					limit = 50
				}

				channel := config.Channel{ID: testOwner, Indexing: &config.Selection{Mode: mode, Limit: limit}}

				work := func() error {
					if previous {
						return a.Catalog.ReconcileDiscovery(b.Context(), run, "uploads", testOwner, limit)
					}

					return a.Catalog.ReconcileUploadRules(b.Context(), run, channel)
				}
				if err = work(); err != nil {
					b.Fatal(err)
				}

				b.ReportAllocs()
				b.ResetTimer()

				for b.Loop() {
					if err = work(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
