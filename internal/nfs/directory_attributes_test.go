package nfs

import (
	"context"
	"sync/atomic"
	"testing"

	"duplex/internal/library"
)

type attributeFixture struct {
	fixture
	stats atomic.Int64
}

func (p *attributeFixture) Stat(ctx context.Context, name string) (library.Node, error) {
	p.stats.Add(1)
	return p.fixture.Stat(ctx, name)
}

func (p *attributeFixture) ReadDirAttributes(ctx context.Context, name, after string, limit int) ([]library.Child, error) {
	children, err := p.ReadDir(ctx, name, after, limit)
	if err != nil {
		return nil, err
	}

	for i := range children {
		info, err := p.fixture.Stat(ctx, children[i].Name)
		if err != nil {
			return nil, err
		}

		children[i].Info = &info
	}

	return children, nil
}

func TestDirectoryAttributesAvoidChildStatsAndKeepStaleHandleChecks(t *testing.T) {
	p := &attributeFixture{fixture: fixture{count: 32}}

	s, err := Listen(t.Context(), "127.0.0.1:0", t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	root := rootHandle(t, s)

	p.stats.Store(0)

	var args encoder
	args.opaque(root)
	args.u64(0)
	args.u64(0)
	args.u32(32768)
	args.u32(32768)

	if code := call(t, s, 100003, 17, args).u32(); code != 0 || p.stats.Load() != 1 {
		t.Fatal("directory page repeated child Stat calls", code, p.stats.Load())
	}

	info, err := p.fixture.Stat(t.Context(), "Recording-00000.bin")
	if err != nil {
		t.Fatal(err)
	}

	handle := s.handles.describe("Recording-00000.bin", info).id
	args = nil
	args.opaque(handle)

	if code := call(t, s, 100003, 1, args).u32(); code != 0 {
		t.Fatal("issued handle was not committed", code)
	}

	p.version.Add(1)

	if code := call(t, s, 100003, 1, args).u32(); code != 70 {
		t.Fatal("batched attributes hid replacement", code)
	}
}
