package nfs

import (
	"context"
	"sync/atomic"
	"testing"

	"duplex/internal/media"
)

type deferredProvider struct {
	fixture
	paused atomic.Bool
}

func (p *deferredProvider) OpenVersion(ctx context.Context, name, revision string) (media.Representation, error) {
	rep, err := p.fixture.OpenVersion(ctx, name, revision)
	if err != nil {
		return nil, err
	}

	return deferredBytes{Representation: rep, paused: &p.paused, deferRead: name == "Recording-00000.bin"}, nil
}

type deferredBytes struct {
	media.Representation
	paused    *atomic.Bool
	deferRead bool
}

func (r deferredBytes) ReadAt(ctx context.Context, p []byte, off int64) (int, error) {
	if r.deferRead && r.paused.Load() {
		return 0, media.ErrDeferred
	}

	return r.Representation.ReadAt(ctx, p, off)
}

func TestDeferredReadIsRetryableAndDoesNotBlockOtherMedia(t *testing.T) {
	provider := &deferredProvider{fixture: fixture{count: 2}}
	provider.paused.Store(true)

	server, err := Listen(t.Context(), "127.0.0.1:0", t.TempDir(), provider)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})

	var calls []encoder

	for _, name := range []string{"Recording-00000.bin", "Recording-00001.bin"} {
		entry, err := server.describe(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}

		if err = server.handles.save(t.Context(), []record{entry}); err != nil {
			t.Fatal(err)
		}

		var args encoder
		args.opaque(entry.id)
		args.u64(0)
		args.u32(16)
		calls = append(calls, args)
	}

	for range 64 {
		response := call(t, server, 100003, 6, calls[0])
		if code := response.u32(); code != 10008 {
			t.Fatal("temporary deferral became an IO error", code)
		}

		response = call(t, server, 100003, 6, calls[1])
		if code := response.u32(); code != 0 {
			t.Fatal("other media was blocked", code)
		}
	}

	provider.paused.Store(false)

	if code := call(t, server, 100003, 6, calls[0]).u32(); code != 0 {
		t.Fatal("deferred read did not recover", code)
	}

	if status(media.ErrDeferred) != 10008 {
		t.Fatal("open deferral was not retryable")
	}
}
