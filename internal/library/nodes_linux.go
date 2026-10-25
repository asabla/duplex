//go:build linux

package library

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"syscall"

	"duplex/internal/media"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

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
