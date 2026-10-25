//go:build linux

package library

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Mount owns one read-only Linux FUSE server.
type Mount struct {
	server   *fuse.Server
	provider Provider
	tree     atomic.Pointer[snapshot]
	path     string
}

// Ready checks the mounted filesystem type before scans are permitted.
func (m *Mount) Ready() bool {
	var stat syscall.Statfs_t
	return syscall.Statfs(m.path, &stat) == nil && stat.Type == 0x65735546
}

// Close unmounts the library after active workers stop.
func (m *Mount) Close() error { return m.server.Unmount() }

// prepareMount refuses live or unrelated mounts. Only a disconnected duplex
// mount can be detached after a process crash.
func prepareMount(mountpoint string) error {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(data), "\n") {
		halves := strings.SplitN(line, " - ", 2)
		if len(halves) != 2 {
			continue
		}
		left, right := strings.Fields(halves[0]), strings.Fields(halves[1])
		if len(left) < 5 || len(right) < 2 || unescape.Replace(left[4]) != mountpoint {
			continue
		}
		if right[0] != "fuse.duplex" || right[1] != "duplex" {
			return errors.New("mount directory is already in use by another filesystem")
		}
		var st syscall.Statfs_t
		err = syscall.Statfs(mountpoint, &st)
		if !errors.Is(err, syscall.ENOTCONN) {
			return errors.New("duplex mount is already active")
		}
		if err = syscall.Unmount(mountpoint, syscall.MNT_DETACH); err != nil {
			return fmt.Errorf("cannot detach disconnected duplex mount: %w", err)
		}
	}
	if info, err := os.Lstat(mountpoint); err == nil && info.Mode()&os.ModeSymlink != 0 { //nolint:gosec // G703: The operator selects the mount path; symlink targets are explicitly rejected.
		return errors.New("mount directory must not be a symbolic link")
	}
	return os.MkdirAll(mountpoint, 0o755) //nolint:gosec // G301: Plex requires traversal through the public read-only mount directory.
}

// MountReadOnly exposes a static qualification snapshot.
func MountReadOnly(mountpoint string, entries []Entry) (*Mount, error) {
	return mountReadOnly(mountpoint, entries, nil)
}

// MountProvider exposes a catalogue-backed provider without a full memory snapshot.
func MountProvider(mountpoint string, provider Provider) (*Mount, error) {
	if provider == nil {
		return nil, errors.New("library provider is required")
	}
	return mountReadOnly(mountpoint, nil, provider)
}

func mountReadOnly(mountpoint string, entries []Entry, provider Provider) (*Mount, error) {
	if e := prepareMount(mountpoint); e != nil {
		return nil, e
	}
	m := &Mount{path: mountpoint, provider: provider}
	if e := m.Update(entries); e != nil {
		return nil, e
	}
	timeout := time.Duration(0)
	server, e := fs.Mount(mountpoint, &node{mount: m}, &fs.Options{AttrTimeout: &timeout, EntryTimeout: &timeout, MountOptions: fuse.MountOptions{Options: []string{"ro"}, AllowOther: true, DirectMount: true, FsName: "duplex", Name: "duplex", DisableXAttrs: true}, RootStableAttr: &fs.StableAttr{Ino: Inode("duplex-root")}})
	if e != nil {
		return nil, e
	}
	m.server = server
	return m, nil
}
