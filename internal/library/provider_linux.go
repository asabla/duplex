//go:build linux

package library

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"syscall"
	"time"

	fusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

func providerErrno(err error) syscall.Errno {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return syscall.EINTR
	default:
		return syscall.EIO
	}
}

func (n *node) providerAttr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	info, err := n.mount.provider.Stat(ctx, n.name)
	if err != nil {
		return providerErrno(err)
	}

	return applyAttr(out, info)
}

func applyAttr(out *fuse.AttrOut, info Node) syscall.Errno {
	out.Mode = syscall.S_IFREG | 0o444

	out.Nlink = 1
	if info.Directory {
		out.Mode = syscall.S_IFDIR | 0o555
		out.Nlink = 2
	}

	if info.Size < 0 || info.Modified < 0 {
		return syscall.EIO
	}

	out.Size = uint64(info.Size)
	out.Mtime = uint64(info.Modified)
	out.Ctime = out.Mtime

	return 0
}

type providerStream struct {
	ctx              context.Context
	cancel           context.CancelFunc
	provider         Provider
	directory, after string
	page             []Child
	err              error
	complete         bool
}

func newProviderStream(ctx context.Context, provider Provider, directory string) fusefs.DirStream {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &providerStream{ctx: ctx, cancel: cancel, provider: provider, directory: directory}
}

func (s *providerStream) HasNext() bool {
	if len(s.page) > 0 || s.err != nil {
		return true
	}

	if s.complete {
		return false
	}

	const size = 128

	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	s.page, s.err = s.provider.ReadDir(ctx, s.directory, s.after, size)

	cancel()

	s.complete = len(s.page) < size
	if len(s.page) > size {
		s.err = errors.New("library provider returned an unbounded page")
		s.page = nil
	}

	return len(s.page) > 0 || s.err != nil
}

func (s *providerStream) Next() (fuse.DirEntry, syscall.Errno) {
	if s.err != nil {
		err := s.err
		s.err = nil
		s.complete = true

		return fuse.DirEntry{}, providerErrno(err)
	}

	if !s.HasNext() {
		return fuse.DirEntry{}, syscall.ENOENT
	}

	// HasNext may have loaded a provider error.
	if s.err != nil {
		return s.Next()
	}

	child := s.page[0]

	s.page = s.page[1:]
	if child.Name <= s.after || path.Base(child.Name) != child.Name || child.Name == "." || child.Name == ".." {
		s.complete = true
		s.page = nil

		return fuse.DirEntry{}, syscall.EIO
	}

	s.after = child.Name

	mode := uint32(syscall.S_IFREG)
	if child.Directory {
		mode = syscall.S_IFDIR
	}

	return fuse.DirEntry{Name: child.Name, Mode: mode, Ino: Inode(path.Join(s.directory, child.Name))}, 0
}

func (s *providerStream) Close() {
	s.cancel()
	s.page = nil
	s.err = nil
	s.complete = true
}
