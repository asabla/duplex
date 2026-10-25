//go:build linux || darwin

package cache

import (
	"errors"
	"math"
	"os"
	"syscall"
)

func allocated(info os.FileInfo) int64 {
	size := info.Size()
	if size < 0 || size > math.MaxInt64-4095 {
		return math.MaxInt64
	}
	rounded := ((size + 4095) / 4096) * 4096
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if stat.Blocks < 0 || stat.Blocks > math.MaxInt64/512 {
			return math.MaxInt64
		}
		return max(stat.Blocks*512, rounded)
	}
	return rounded
}

func diskSpace(dir string) (free, total int64, err error) {
	var stat syscall.Statfs_t
	if err = syscall.Statfs(dir, &stat); err != nil {
		return
	}
	free, err = capacity(uint64(stat.Bavail), uint64(stat.Bsize))
	if err != nil {
		return 0, 0, err
	}
	total, err = capacity(uint64(stat.Blocks), uint64(stat.Bsize))
	return free, total, err
}

func capacity(blocks, size uint64) (int64, error) {
	if size == 0 || blocks > math.MaxInt64/size {
		return 0, errors.New("unsupported filesystem capacity")
	}
	return int64(blocks * size), nil
}
