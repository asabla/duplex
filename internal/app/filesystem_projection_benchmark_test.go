package app

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"strings"
	"testing"

	"duplex/internal/library"
)

// This workload also compiles against the prior provider contract, so the
// before and after binaries execute the same scan-shaped benchmark.
func BenchmarkProjectionDirectoryAttributes(b *testing.B) {
	a := benchmarkMetadataCatalogue(b, 10000, true)
	provider := catalogueFS{app: a}

	for _, directory := range []string{"Channel-" + testOwner + "/Season 2026", "Channel-" + testOwner + "/Season 0001"} {
		b.Run(path.Base(directory), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if batched, ok := any(provider).(interface {
					ReadDirAttributes(context.Context, string, string, int) ([]library.Child, error)
				}); ok {
					page, err := batched.ReadDirAttributes(b.Context(), directory, "", 128)
					if err != nil || len(page) != 128 {
						b.Fatal("attribute page", len(page), err)
					}
				} else {
					page, err := provider.ReadDir(b.Context(), directory, "", 128)
					if err != nil || len(page) != 128 {
						b.Fatal("directory page", len(page), err)
					}

					for _, child := range page {
						if _, err = provider.Stat(b.Context(), path.Join(directory, child.Name)); err != nil {
							b.Fatal(err)
						}
					}
				}
			}
		})
	}
}

func BenchmarkProjectionReadAmplification(b *testing.B) {
	a := benchmarkCatalogue(b, 10000)
	provider := catalogueFS{app: a}
	operations := metadataOperations(false)
	nfo := operations[3].path

	info, err := provider.Stat(b.Context(), nfo)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("nfo_versioned_open", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			if _, err := provider.OpenVersion(b.Context(), nfo, info.Revision); err != nil {
				b.Fatal(err)
			}
		}
	})

	missing := strings.TrimSuffix(operations[2].path, ".mp4") + ".unavailable"

	b.Run("missing_sidecar", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			if _, err := provider.Stat(b.Context(), missing); !errors.Is(err, fs.ErrNotExist) {
				b.Fatal(err)
			}
		}
	})
}
