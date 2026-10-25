package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkPublishedChunkRead(b *testing.B) {
	const length = 1 << 20
	file := filepath.Join(b.TempDir(), "published.chunk")
	if err := os.WriteFile(file, make([]byte, length), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(length)
	for b.Loop() {
		data, err := readChunkFile(file, length)
		if err != nil || len(data) != length {
			b.Fatal(len(data), err)
		}
	}
}
