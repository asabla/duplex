//go:build linux

package library

import (
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

type snapshot struct {
	files    map[string]Entry
	dirs     map[string][]fuse.DirEntry
	modified int64
}

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

func validEntry(e Entry) bool {
	return e.Media != nil && e.Media.Size() >= 0 && e.Modified >= 0 && e.Path != "" && e.Path != "." && e.Path != ".." && path.Clean(e.Path) == e.Path && !strings.HasPrefix(e.Path, "/") && !strings.HasPrefix(e.Path, "../")
}
