package app

import (
	"fmt"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/config"
)

func benchmarkCatalogue(b *testing.B, count int) *App {
	b.Helper()

	app := pipelineApp(b, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	if err := app.Catalog.EnsureChannel(b.Context(), testOwner, "Benchmark channel"); err != nil {
		b.Fatal(err)
	}

	_, err := app.Catalog.DB.ExecContext(b.Context(), `WITH RECURSIVE items(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM items WHERE n<?) INSERT INTO videos(id,channel_id,title,state,published,year,number,exposed,revision,height,size,updated,selected,preparation,metadata_at,search_title,requested_height) SELECT printf('video%06d',n),?,'Benchmark recording','recorded',1700000000,2026,n,1,'verified',1080,1048576,1700000000,1,'ready',1700000000,'benchmark recording',2160 FROM items`, count, testOwner)
	if err != nil {
		b.Fatal(err)
	}

	return app
}

func BenchmarkCatalogueFilesystemPage(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			app := benchmarkCatalogue(b, count)
			provider := catalogueFS{app: app}
			directory := "Channel-" + testOwner + "/Season 2026"

			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				page, err := provider.ReadDir(b.Context(), directory, "", 128)
				if err != nil || len(page) != 128 {
					b.Fatal(len(page), err)
				}
			}
		})
	}
}

func BenchmarkPublish(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			app := benchmarkCatalogue(b, count)
			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				if err := app.Publish(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCataloguePlaylistFilesystemPage(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			app := benchmarkCatalogue(b, count)

			if err := app.Catalog.SavePlaylist(b.Context(), catalog.Playlist{ID: "PLbenchmark", ChannelID: testOwner, Title: "Benchmark playlist"}); err != nil {
				b.Fatal(err)
			}

			_, err := app.Catalog.DB.ExecContext(b.Context(), `INSERT INTO playlist_members(playlist_id,video_id,position,selected,episode,file_episode) SELECT 'PLbenchmark',id,number,1,number,number FROM videos`)
			if err != nil {
				b.Fatal(err)
			}

			provider := catalogueFS{app: app}
			directory := "Channel-" + testOwner + "/Season 0001"

			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				page, err := provider.ReadDir(b.Context(), directory, "", 128)
				if err != nil || len(page) != 128 {
					b.Fatal(len(page), err)
				}
			}
		})
	}
}
