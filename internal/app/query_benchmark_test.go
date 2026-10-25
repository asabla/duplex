package app

import (
	"fmt"
	"path/filepath"
	"testing"

	"duplex/internal/cache"
	"duplex/internal/catalog"
)

// BenchmarkCatalogueReads measures the queries used by progress polling and
// the video page against the same durable fixture as filesystem enumeration.
func BenchmarkCatalogueReads(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			app := benchmarkReadApp(b, count)
			b.Run("Summary", func(b *testing.B) {
				b.ReportAllocs()

				for b.Loop() {
					_, counts, err := app.Summary(b.Context())
					if err != nil || counts.Ready != count {
						b.Fatal(counts, err)
					}
				}
			})

			for _, inclusion := range []string{"selected", "all"} {
				b.Run("Videos/"+inclusion, func(b *testing.B) {
					b.ReportAllocs()

					for b.Loop() {
						page, err := app.Videos(b.Context(), catalog.VideoFilter{Inclusion: inclusion, PerPage: 25})
						if err != nil || page.Total != count || len(page.Items) != 25 {
							b.Fatal(page.Total, len(page.Items), err)
						}
					}
				})
			}
		})
	}
}

func BenchmarkCatalogueSummaryLatest50(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			app := benchmarkReadApp(b, count)
			if _, err := app.Catalog.DB.ExecContext(b.Context(), `UPDATE videos SET selected=(number<=50)`); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				_, counts, err := app.Summary(b.Context())
				if err != nil || counts.Discovered != count || counts.Selected != 50 || counts.Ready != 50 {
					b.Fatal(counts, err)
				}
			}
		})
	}
}

func BenchmarkCatalogueSelectedPages(b *testing.B) {
	const count = 10000

	for _, scenario := range []string{"latest50", "shared_playlists"} {
		b.Run(scenario, func(b *testing.B) {
			app := benchmarkReadApp(b, count)
			if _, err := app.Catalog.DB.ExecContext(b.Context(), `UPDATE videos SET published=published+number`); err != nil {
				b.Fatal(err)
			}

			selected := count
			if scenario == "latest50" {
				selected = 50

				if _, err := app.Catalog.DB.ExecContext(b.Context(), `UPDATE videos SET selected=(number>9950)`); err != nil {
					b.Fatal(err)
				}
			} else {
				for _, id := range []string{"PLbenchmark001", "PLbenchmark002"} {
					if err := app.Catalog.SavePlaylist(b.Context(), catalog.Playlist{ID: id, ChannelID: testOwner, Title: id}); err != nil {
						b.Fatal(err)
					}

					if _, err := app.Catalog.DB.ExecContext(b.Context(), `INSERT INTO playlist_members(playlist_id,video_id,position,selected,episode,file_episode,updated) SELECT ?,id,number,1,number,number,updated FROM videos`, id); err != nil {
						b.Fatal(err)
					}
				}
			}

			for _, pageNumber := range []int{1, selected / 25} {
				b.Run(fmt.Sprint(pageNumber), func(b *testing.B) {
					b.ReportAllocs()

					for b.Loop() {
						page, err := app.Videos(b.Context(), catalog.VideoFilter{Inclusion: "selected", PerPage: 25, Page: pageNumber})
						if err != nil || page.Total != selected || len(page.Items) != 25 || page.Page != pageNumber {
							b.Fatal(page.Total, len(page.Items), page.Page, err)
						}
					}
				})
			}
		})
	}
}

func benchmarkReadApp(b *testing.B, count int) *App {
	b.Helper()
	app := benchmarkCatalogue(b, count)

	var err error

	app.Cache, err = cache.New(filepath.Join(b.TempDir(), "cache"), 1<<20)
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		if err := app.Cache.Close(); err != nil {
			b.Error(err)
		}
	})

	return app
}
