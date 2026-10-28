//go:build linux

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"duplex/internal/library"
)

// BenchmarkMountedFilesystemMetadata measures complete path lookup, including
// parent lookups. Use a fixed 500 iterations for the latency percentiles.
func BenchmarkMountedFilesystemMetadata(b *testing.B) {
	if os.Getenv("DUPLEX_FUSE_TEST") != "1" {
		b.Skip("requires explicit Linux FUSE qualification")
	}

	for _, playlist := range []bool{false, true} {
		b.Run(fmt.Sprintf("playlist_%t", playlist), func(b *testing.B) {
			a := benchmarkMetadataCatalogue(b, 10000, playlist)
			provider := catalogueFS{app: a}
			mounted := filepath.Join(b.TempDir(), "mount")

			mount, err := library.MountProvider(mounted, provider)
			if err != nil {
				b.Fatal(err)
			}

			b.Cleanup(func() {
				if err := mount.Close(); err != nil {
					b.Error(err)
				}
			})
			native := benchmarkNativeMetadata(b, provider, playlist)

			roots := []struct{ name, path string }{{"native", native}, {"fuse", mounted}}
			if os.Getenv("DUPLEX_NFS_TEST") == "1" {
				roots = append(roots, struct{ name, path string }{"nfs", benchmarkNFSMount(b, provider, 1)}, struct{ name, path string }{"nfs_no_attribute_cache", benchmarkNFSMount(b, provider, 0)})
			}

			for _, root := range roots {
				for _, operation := range metadataOperations(playlist) {
					b.Run(root.name+"/"+operation.name, func(b *testing.B) {
						benchmarkMetadataStat(b, filepath.Join(root.path, operation.path))
					})
				}
			}
		})
	}
}

func benchmarkNativeMetadata(b *testing.B, provider catalogueFS, playlist bool) string {
	b.Helper()

	root := b.TempDir()
	for _, operation := range metadataOperations(playlist) {
		info, err := provider.Stat(b.Context(), operation.path)
		if err != nil {
			b.Fatal(err)
		}

		name := filepath.Join(root, operation.path)
		if info.Directory {
			err = os.MkdirAll(name, 0o700)
		} else {
			err = os.WriteFile(name, nil, 0o600)
		}

		if err != nil {
			b.Fatal(err)
		}
	}

	episode := metadataOperations(playlist)[2].path
	base := episode[:len(episode)-len("0001.mp4")]

	for number := 1; number <= 10000; number++ {
		for _, suffix := range []string{".mp4", ".nfo"} {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%s%04d%s", base, number, suffix)), nil, 0o600); err != nil {
				b.Fatal(err)
			}
		}
	}

	return root
}

func benchmarkMetadataStat(b *testing.B, name string) {
	samples := make([]int64, 0, 4096)

	b.ReportAllocs()

	for b.Loop() {
		started := time.Now()

		if _, err := os.Stat(name); err != nil {
			b.Fatal(err)
		}

		if len(samples) < cap(samples) {
			samples = append(samples, time.Since(started).Nanoseconds())
		}
	}

	slices.Sort(samples)

	for _, percentile := range []int{50, 95, 99} {
		index := (len(samples)*percentile+99)/100 - 1
		b.ReportMetric(float64(samples[index]), fmt.Sprintf("p%d-ns", percentile))
	}
}
