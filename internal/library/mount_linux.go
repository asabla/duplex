//go:build linux

package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"duplex/internal/media"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type snapshot struct {
	files    map[string]Entry
	dirs     map[string][]fuse.DirEntry
	modified int64
}

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

// Update atomically replaces a static qualification snapshot.
func (m *Mount) Update(entries []Entry) error {
	s := &snapshot{files: map[string]Entry{}, dirs: map[string][]fuse.DirEntry{"": {}}, modified: max(int64(0), time.Now().Unix())}
	children := map[string]map[string]uint32{}
	for _, e := range entries {
		if err := s.addEntry(e, children); err != nil {
			return err
		}
	}
	for dir, names := range children {
		for name, mode := range names {
			s.dirs[dir] = append(s.dirs[dir], fuse.DirEntry{Name: name, Mode: mode, Ino: Inode(path.Join(dir, name))})
		}
		sort.Slice(s.dirs[dir], func(i, j int) bool { return s.dirs[dir][i].Name < s.dirs[dir][j].Name })
	}
	m.tree.Store(s)
	return nil
}

func (s *snapshot) addEntry(e Entry, children map[string]map[string]uint32) error {
	if !validEntry(e) {
		return syscall.EINVAL
	}
	if _, exists := s.files[e.Path]; exists {
		return syscall.EINVAL
	}
	s.files[e.Path] = e
	parts := strings.Split(e.Path, "/")
	parent := ""
	for i, p := range parts {
		mode := uint32(syscall.S_IFDIR)
		if i == len(parts)-1 {
			mode = syscall.S_IFREG
		}
		if children[parent] == nil {
			children[parent] = map[string]uint32{}
		}
		if previous, exists := children[parent][p]; exists && previous != mode {
			return syscall.EINVAL
		}
		children[parent][p] = mode
		parent = path.Join(parent, p)
		if mode == syscall.S_IFDIR {
			s.dirs[parent] = nil
		}
	}
	return nil
}

type node struct {
	fs.Inode
	mount *Mount
	name  string
}

func (n *node) Getattr(ctx context.Context, fileHandle fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if h, ok := fileHandle.(*handle); ok {
		return applyAttr(out, Node{Size: h.rep.Size(), Modified: h.modified})
	}
	if n.mount.provider != nil {
		return n.providerAttr(ctx, out)
	}
	s := n.mount.tree.Load()
	if e, ok := s.files[n.name]; ok {
		modified := e.Modified
		if modified == 0 {
			modified = s.modified
		}
		return applyAttr(out, Node{Size: e.Media.Size(), Modified: modified})
	}
	if _, ok := s.dirs[n.name]; ok {
		return applyAttr(out, Node{Directory: true, Modified: s.modified})
	}
	return syscall.ENOENT
}

func validEntry(e Entry) bool {
	return e.Media != nil && e.Media.Size() >= 0 && e.Modified >= 0 && e.Path != "" && e.Path != "." && e.Path != ".." && path.Clean(e.Path) == e.Path && !strings.HasPrefix(e.Path, "/") && !strings.HasPrefix(e.Path, "../")
}

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name == "." || name == ".." || strings.Contains(name, "/") {
		return nil, syscall.ENOENT
	}
	p := path.Join(n.name, name)
	child := &node{mount: n.mount, name: p}
	var attr fuse.AttrOut
	if code := child.Getattr(ctx, nil, &attr); code != 0 {
		return nil, code
	}
	out.Attr = attr.Attr
	return n.NewInode(ctx, child, fs.StableAttr{Mode: out.Mode & syscall.S_IFMT, Ino: Inode(p)}), 0
}

func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if n.mount.provider != nil {
		return newProviderStream(ctx, n.mount.provider, n.name), 0
	}
	s := n.mount.tree.Load()
	entries, ok := s.dirs[n.name]
	if !ok {
		return nil, syscall.ENOTDIR
	}
	return fs.NewListDirStream(entries), 0
}

func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_APPEND) != 0 {
		return nil, 0, syscall.EROFS
	}
	if n.mount.provider != nil {
		rep, modified, err := n.mount.provider.Open(ctx, n.name)
		if err != nil {
			return nil, 0, providerErrno(err)
		}
		return &handle{rep: rep, modified: modified}, fuse.FOPEN_DIRECT_IO, 0
	}

	s := n.mount.tree.Load()
	e, ok := s.files[n.name]
	if !ok {
		return nil, 0, syscall.ENOENT
	}
	modified := e.Modified
	if modified == 0 {
		modified = s.modified
	}
	return &handle{rep: e.Media, modified: modified}, fuse.FOPEN_DIRECT_IO, 0
}

type handle struct {
	rep      media.Representation
	modified int64
}

func (h *handle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n, e := h.rep.ReadAt(ctx, dest, off)
	if e != nil && !errors.Is(e, io.EOF) {
		if ctx.Err() != nil {
			return nil, syscall.EINTR
		}
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

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
