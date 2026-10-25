//go:build linux || darwin

package cache

import (
	"math"
	"os"
	"syscall"
	"testing"
)

type allocationInfo struct {
	os.FileInfo
	length int64
	system any
}

func (i allocationInfo) Size() int64 { return i.length }
func (i allocationInfo) Sys() any    { return i.system }
func TestAllocationAndCapacityBounds(t *testing.T) {
	for _, size := range []int64{-1, math.MaxInt64} {
		if got := allocated(allocationInfo{length: size}); got != math.MaxInt64 {
			t.Fatal("allocation overflow", got)
		}
	}

	if got := allocated(allocationInfo{length: 1}); got != 4096 {
		t.Fatal(got)
	}

	if got := allocated(allocationInfo{length: 1, system: &syscall.Stat_t{Blocks: -1}}); got != math.MaxInt64 {
		t.Fatal(got)
	}

	if _, err := capacity(math.MaxUint64, 4096); err == nil {
		t.Fatal("filesystem capacity overflow accepted")
	}

	if _, err := capacity(1, 0); err == nil {
		t.Fatal("zero block size accepted")
	}

	if free, err := capacity(2, 4096); err != nil || free != 8192 {
		t.Fatal(free, err)
	}
}
