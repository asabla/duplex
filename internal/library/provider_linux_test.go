//go:build linux

package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"

	"duplex/internal/media"
)

type fixtureProvider struct {
	mu        sync.Mutex
	files     map[string]blob
	pageSizes []int
}

func (p *fixtureProvider) Stat(ctx context.Context, name string) (Node, error) {
	if err := ctx.Err(); err != nil {
		return Node{}, err
	}

	if name == "" || name == "Channel" {
		return Node{Directory: true, Modified: 1700000000}, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	value, ok := p.files[name]
	if !ok {
		return Node{}, fs.ErrNotExist
	}

	return Node{Size: value.Size(), Modified: 1700000000}, nil
}

func (p *fixtureProvider) ReadDir(ctx context.Context, name, after string, limit int) ([]Child, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.pageSizes = append(p.pageSizes, limit)

	if name == "" {
		if "Channel" > after {
			return []Child{{Name: "Channel", Directory: true}}, nil
		}

		return nil, nil
	}

	if name != "Channel" {
		return nil, fs.ErrNotExist
	}

	children := []Child{}

	for file := range p.files {
		child := strings.TrimPrefix(file, "Channel/")
		if child > after {
			children = append(children, Child{Name: child})
		}
	}

	sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })

	return children[:min(limit, len(children))], nil
}

func (p *fixtureProvider) Open(ctx context.Context, name string) (media.Representation, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	value, ok := p.files[name]
	if !ok {
		return nil, 0, fs.ErrNotExist
	}

	return value, 1700000000, nil
}

func TestFuseProviderPagesAndImmutableHandles(t *testing.T) {
	if os.Getenv("DUPLEX_FUSE_TEST") != "1" {
		t.Skip("requires explicit Linux FUSE qualification")
	}

	provider := &fixtureProvider{files: map[string]blob{}}

	for n := range 300 {
		key := fmt.Sprintf("Channel/episode%04d.mp4", n)
		provider.files[key] = blob{id: key, data: []byte("original bytes")}
	}

	mountpoint := filepath.Join(t.TempDir(), "library")

	mount, err := MountProvider(mountpoint, provider)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := mount.Close(); err != nil {
			t.Error(err)
		}
	}()

	entries, err := os.ReadDir(filepath.Join(mountpoint, "Channel"))
	if err != nil || len(entries) != 300 {
		t.Fatal(len(entries), err)
	}

	provider.mu.Lock()
	sizes := append([]int(nil), provider.pageSizes...)
	provider.mu.Unlock()

	if len(sizes) < 3 {
		t.Fatal("directory was not paged", sizes)
	}

	for _, size := range sizes {
		if size > 128 {
			t.Fatal("unbounded provider page", size)
		}
	}

	name := filepath.Join(mountpoint, "Channel", "episode0000.mp4")

	old, err := os.Open(name) //nolint:gosec // G304: This is the controlled provider mount in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := old.Close(); err != nil {
			t.Error(err)
		}
	}()

	provider.mu.Lock()
	provider.files["Channel/episode0000.mp4"] = blob{id: "replacement", data: []byte("new immutable representation")}
	provider.mu.Unlock()

	fresh, err := os.ReadFile(name) //nolint:gosec // G304: This is the controlled provider mount in t.TempDir.
	if err != nil || string(fresh) != "new immutable representation" {
		t.Fatal(string(fresh), err)
	}

	original, err := io.ReadAll(old)
	if err != nil || string(original) != "original bytes" {
		t.Fatal("open handle changed", string(original), err)
	}

	if _, err = os.OpenFile(name, os.O_WRONLY, 0); err == nil { //nolint:gosec // G304: This is the controlled provider mount in t.TempDir.
		t.Fatal("provider mount accepted a write")
	}

	provider.mu.Lock()
	delete(provider.files, "Channel/episode0000.mp4")
	provider.mu.Unlock()

	if _, err = os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("removed entry remained visible", err)
	}

	data := make([]byte, len("original bytes"))
	if _, err = old.ReadAt(data, 0); err != nil || string(data) != "original bytes" {
		t.Fatal("unlinked handle changed", string(data), err)
	}
}

func TestSnapshotRejectsUnsafePathsWithoutReplacingPublishedState(t *testing.T) {
	mount := &Mount{}

	valid := Entry{Path: "Channel/episode.mp4", Media: blob{id: "valid", data: []byte("bytes")}}
	if err := mount.Update([]Entry{valid}); err != nil {
		t.Fatal(err)
	}

	published := mount.tree.Load()

	for _, name := range []string{"", ".", "..", "../escape", "/absolute", "a/../escape", "a//b"} {
		entry := valid

		entry.Path = name
		if err := mount.Update([]Entry{entry}); err == nil || mount.tree.Load() != published {
			t.Fatal("unsafe path replaced publication", name, err)
		}
	}

	for _, entries := range [][]Entry{{valid, valid}, {{Path: "Channel", Media: valid.Media}, valid}, {valid, {Path: "Channel", Media: valid.Media}}, {{Path: "nil-media"}}} {
		if err := mount.Update(entries); err == nil || mount.tree.Load() != published {
			t.Fatal("invalid tree replaced publication", err)
		}
	}

	if _, err := MountProvider("", nil); err == nil {
		t.Fatal("nil provider accepted")
	}
}

func TestHandleReturnsSourceFailuresAndExactEndReads(t *testing.T) {
	failure := errors.New("source failed")

	h := &handle{rep: failedRange{failure}}
	if _, code := h.Read(t.Context(), make([]byte, 4), 0); code != syscall.EIO {
		t.Fatal("source failure became successful data", code)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, code := h.Read(ctx, make([]byte, 4), 0); code != syscall.EINTR {
		t.Fatal("cancellation not propagated", code)
	}

	h.rep = blob{id: "short", data: []byte("end")}

	result, code := h.Read(t.Context(), make([]byte, 8), 0)
	if code != 0 || result.Size() != 3 {
		t.Fatal(result, code)
	}

	result.Done()
}
