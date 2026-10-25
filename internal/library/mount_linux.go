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
	modified uint64
}
type Mount struct {
	server *fuse.Server
	tree   atomic.Pointer[snapshot]
	path   string
}

func (m *Mount) Ready() bool {
	var stat syscall.Statfs_t
	return syscall.Statfs(m.path, &stat) == nil && uint64(stat.Type) == 0x65735546
}

func (m *Mount) Close() error { return m.server.Unmount() }
func (m *Mount) Update(entries []Entry) error {
	s := &snapshot{files: map[string]Entry{}, dirs: map[string][]fuse.DirEntry{"": {}}, modified: uint64(time.Now().Unix())}
	children := map[string]map[string]uint32{}
	for _, e := range entries {
		if e.Media == nil || path.Clean(e.Path) != e.Path || strings.HasPrefix(e.Path, "/") || strings.HasPrefix(e.Path, "../") {
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
			children[parent][p] = mode
			parent = path.Join(parent, p)
			if mode == syscall.S_IFDIR {
				s.dirs[parent] = nil
			}
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

type node struct {
	fs.Inode
	mount *Mount
	name  string
}

func (n *node) Getattr(_ context.Context, fileHandle fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if h, ok := fileHandle.(*handle); ok {
		out.Mode = syscall.S_IFREG | 0444
		out.Size = uint64(h.rep.Size())
		out.Nlink = 1
		out.Mtime = uint64(h.modified)
		out.Ctime = out.Mtime
		return 0
	}
	s := n.mount.tree.Load()
	if e, ok := s.files[n.name]; ok {
		out.Mode = syscall.S_IFREG | 0444
		out.Size = uint64(e.Media.Size())
		out.Nlink = 1
		out.Mtime = uint64(e.Modified)
		if out.Mtime == 0 {
			out.Mtime = s.modified
		}
	} else if _, ok := s.dirs[n.name]; ok {
		out.Mode = syscall.S_IFDIR | 0555
		out.Nlink = 2
		out.Mtime = s.modified
	} else {
		return syscall.ENOENT
	}
	out.Ctime = out.Mtime
	return 0
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
func (n *node) Readdir(context.Context) (fs.DirStream, syscall.Errno) {
	s := n.mount.tree.Load()
	entries, ok := s.dirs[n.name]
	if !ok {
		return nil, syscall.ENOTDIR
	}
	return fs.NewListDirStream(entries), 0
}
func (n *node) Open(_ context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_APPEND) != 0 {
		return nil, 0, syscall.EROFS
	}
	s := n.mount.tree.Load()
	e, ok := s.files[n.name]
	if !ok {
		return nil, 0, syscall.ENOENT
	}
	modified := e.Modified
	if modified == 0 {
		modified = int64(s.modified)
	}
	return &handle{rep: e.Media, modified: modified}, fuse.FOPEN_DIRECT_IO, 0
}

type handle struct {
	rep      media.Representation
	modified int64
}

func (h *handle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n, e := h.rep.ReadAt(ctx, dest, off)
	if e != nil && e != io.EOF {
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
	if info, err := os.Lstat(mountpoint); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("mount directory must not be a symbolic link")
	}
	return os.MkdirAll(mountpoint, 0755)
}
func MountReadOnly(mountpoint string, entries []Entry) (*Mount, error) {
	if e := prepareMount(mountpoint); e != nil {
		return nil, e
	}
	m := &Mount{path: mountpoint}
	if e := m.Update(entries); e != nil {
		return nil, e
	}
	server, e := fs.Mount(mountpoint, &node{mount: m}, &fs.Options{MountOptions: fuse.MountOptions{Options: []string{"ro"}, AllowOther: true, DirectMount: true, FsName: "duplex", Name: "duplex", DisableXAttrs: true}, RootStableAttr: &fs.StableAttr{Ino: Inode("duplex-root")}})
	if e != nil {
		return nil, e
	}
	m.server = server
	return m, nil
}
