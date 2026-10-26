package app

import (
	"fmt"
	"testing"

	"duplex/internal/catalog"
)

func benchmarkMetadataCatalogue(b *testing.B, count int, playlist bool) *App {
	b.Helper()

	a := benchmarkCatalogue(b, count)
	if !playlist {
		return a
	}

	if err := a.Catalog.SavePlaylist(b.Context(), catalog.Playlist{ID: "PLmetadata", ChannelID: testOwner, Title: "Metadata playlist"}); err != nil {
		b.Fatal(err)
	}

	_, err := a.Catalog.DB.ExecContext(b.Context(), `INSERT INTO playlist_members(playlist_id,video_id,position,selected,episode,file_episode,updated) SELECT 'PLmetadata',id,number,1,number,number,updated FROM videos`)
	if err != nil {
		b.Fatal(err)
	}

	return a
}

type metadataOperation struct{ name, path string }

func metadataOperations(playlist bool) []metadataOperation {
	season := 2026
	if playlist {
		season = 1
	}

	channel := "Channel-" + testOwner
	directory := fmt.Sprintf("%s/Season %04d", channel, season)
	episode := fmt.Sprintf("%s/Channel-%s - S%04dE0001", directory, testOwner, season)

	return []metadataOperation{
		{"channel", channel},
		{"season", directory},
		{"media", episode + ".mp4"},
		{"episode_nfo", episode + ".nfo"},
		{"show_nfo", channel + "/tvshow.nfo"},
	}
}

func BenchmarkCatalogueFilesystemMetadata(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		for _, playlist := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/playlist_%t", count, playlist), func(b *testing.B) {
				a := benchmarkMetadataCatalogue(b, count, playlist)
				provider := catalogueFS{app: a}

				for _, operation := range metadataOperations(playlist) {
					b.Run(operation.name, func(b *testing.B) {
						b.ReportAllocs()

						for b.Loop() {
							if _, err := provider.Stat(b.Context(), operation.path); err != nil {
								b.Fatal(err)
							}
						}
					})
				}
			})
		}
	}
}

func BenchmarkCatalogueProjectionUpdate(b *testing.B) {
	for _, playlist := range []bool{false, true} {
		b.Run(fmt.Sprintf("playlist_%t", playlist), func(b *testing.B) {
			a := benchmarkMetadataCatalogue(b, 10000, playlist)
			b.ReportAllocs()

			for b.Loop() {
				if _, err := a.Catalog.DB.ExecContext(b.Context(), `UPDATE videos SET updated=updated+1 WHERE id='video000001'`); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
