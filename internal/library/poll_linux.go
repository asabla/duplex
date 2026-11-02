//go:build linux

package library

import (
	"errors"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Descriptor mounts skip go-fuse's WaitMount polling setup because the server
// does not know their real path. Prime its reserved probe through raw syscalls
// before os.File can register with Go's poller and consume every scheduler slot.
// The probe name is provided by the pinned go-fuse version, not our provider.
func primeMountPolling(mountpoint string) (failure error) {
	fd, err := unix.Open(filepath.Join(mountpoint, ".go-fuse-epoll-hack"), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}

	defer func() { failure = errors.Join(failure, unix.Close(fd)) }()

	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLPRI | unix.POLLOUT}} //nolint:gosec // G115: Linux descriptors are non-negative C int values.
	_, err = unix.Poll(poll, 0)

	return err
}
