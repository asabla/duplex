//go:build linux

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"duplex/internal/library"
)

func BenchmarkMountedMetadataUnderWrites(b *testing.B) {
	if os.Getenv("DUPLEX_FUSE_TEST") != "1" {
		b.Skip("requires explicit Linux FUSE qualification")
	}

	for _, operation := range []metadataOperation{metadataOperations(true)[0], metadataOperations(true)[3], metadataOperations(true)[4]} {
		b.Run(operation.name, func(b *testing.B) {
			a := benchmarkMetadataCatalogue(b, 10000, true)
			root := filepath.Join(b.TempDir(), "mount")

			mount, err := library.MountProvider(root, catalogueFS{app: a})
			if err != nil {
				b.Fatal(err)
			}

			b.Cleanup(func() {
				if err := mount.Close(); err != nil {
					b.Error(err)
				}
			})
			runMetadataWriteWorkload(b, a, func(context.Context) error {
				_, err := os.Stat(filepath.Join(root, operation.path))
				return err
			})
		})
	}
}
