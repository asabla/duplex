package app

import (
	"fmt"
	"path"
	"reflect"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/library"
)

func TestDirectoryAttributesMatchIndividualLookupsAndUpdates(t *testing.T) {
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	for i, number := range []int{1, 9999, 10000, 10001} {
		projectionVideo(t, a, fmt.Sprintf("video%06d", i), number)
	}

	if err := a.Catalog.SavePlaylist(t.Context(), catalog.Playlist{ID: "PLfixture0001", ChannelID: testOwner, Title: "Playlist"}); err != nil {
		t.Fatal(err)
	}

	for i := range 4 {
		if err := a.Catalog.AddPlaylistMember(t.Context(), "PLfixture0001", fmt.Sprintf("video%06d", i), i+1, true); err != nil {
			t.Fatal(err)
		}
	}

	p := catalogueFS{app: a}
	check := func() {
		for _, season := range []int{2026, 1} {
			directory := fmt.Sprintf("Channel-%s/Season %04d", testOwner, season)

			for _, limit := range []int{1, 3, 128} {
				after := ""
				for {
					plain, err := p.ReadDir(t.Context(), directory, after, limit)
					if err != nil {
						t.Fatal(err)
					}

					page, err := p.ReadDirAttributes(t.Context(), directory, after, limit)
					if err != nil || len(page) != len(plain) {
						t.Fatal("attribute page changed membership", err, len(page), len(plain))
					}

					for i, child := range page {
						if child.Name != plain[i].Name {
							t.Fatal("attribute page changed lexical order")
						}

						info, err := p.Stat(t.Context(), path.Join(directory, child.Name))
						if err != nil {
							t.Fatal(err)
						}

						if child.Info != nil && !reflect.DeepEqual(info, *child.Info) {
							t.Fatal("batched attributes differ", child.Name, info, *child.Info)
						}
					}

					if len(page) < limit {
						break
					}

					after = page[len(page)-1].Name
				}
			}
		}
	}
	check()

	var assets []library.Entry

	entry, err := a.saveAsset("video000002", ".en.srt", []byte("local subtitle"))
	if err != nil {
		t.Fatal(err)
	}

	assets = append(assets, entry)
	if err = a.saveAssetManifest("video000002", assets); err != nil {
		t.Fatal(err)
	}

	if _, err = a.Catalog.DB.ExecContext(t.Context(), `UPDATE videos SET title='Changed',description='Changed description',updated=updated+1 WHERE id='video000002'`); err != nil {
		t.Fatal(err)
	}

	if _, err = a.Catalog.DB.ExecContext(t.Context(), `UPDATE playlist_members SET episode=5 WHERE video_id='video000002'`); err != nil {
		t.Fatal(err)
	}

	check()

	if _, err = a.Catalog.DB.ExecContext(t.Context(), `UPDATE videos SET selected=0 WHERE id='video000001'`); err != nil {
		t.Fatal(err)
	}

	check()
}

func TestWarmEpisodeMetadataDoesNotRenderOrWrite(t *testing.T) {
	a := pipelineApp(t, pipelineChannel(config.Selection{Mode: "all"}), fixtureCollection(0))
	projectionVideo(t, a, "video000001", 1)
	p := catalogueFS{app: a}
	name := fmt.Sprintf("Channel-%s/Season 2026/Channel-%s - S2026E0001.nfo", testOwner, testOwner)

	info, err := p.Stat(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}

	var before, after int64
	if err = a.Catalog.DB.QueryRowContext(t.Context(), `SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	renders := int64(0)

	for _, counter := range a.Measurements.Counters() {
		if counter.Name == "filesystem.nfo.render" {
			renders = counter.Value
		}
	}

	for range 50 {
		if _, err = p.OpenVersion(t.Context(), name, info.Revision); err != nil {
			t.Fatal(err)
		}
	}

	if err = a.Catalog.DB.QueryRowContext(t.Context(), `SELECT total_changes()`).Scan(&after); err != nil || before != after {
		t.Fatal("metadata read wrote catalogue", before, after, err)
	}

	for _, counter := range a.Measurements.Counters() {
		if counter.Name == "filesystem.nfo.render" && counter.Value != renders {
			t.Fatal("warm metadata regenerated XML", renders, counter.Value)
		}
	}
}
