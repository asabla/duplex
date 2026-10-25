//go:build linux || darwin

package cache

import (
	"os"
	"syscall"
)

func allocated(info os.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return max(st.Blocks*512, ((info.Size()+4095)/4096)*4096)
	}
	return ((info.Size() + 4095) / 4096) * 4096
}

func diskSpace(dir string) (free, total int64, err error) {
	var stat syscall.Statfs_t
	if err = syscall.Statfs(dir, &stat); err != nil {
		return
	}
	return int64(stat.Bavail) * int64(stat.Bsize), int64(stat.Blocks) * int64(stat.Bsize), nil
}
