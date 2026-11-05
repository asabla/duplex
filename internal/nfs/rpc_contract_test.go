package nfs

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"sync/atomic"
	"testing"

	"duplex/internal/library"
	"duplex/internal/media"
)

type contractProvider struct {
	fixture
	calls   atomic.Int64
	opens   atomic.Int64
	bytes   atomic.Int64
	entered chan struct{}
	gate    <-chan struct{}
}

func (p *contractProvider) Stat(ctx context.Context, name string) (library.Node, error) {
	p.calls.Add(1)
	return p.fixture.Stat(ctx, name)
}

func (p *contractProvider) ReadDir(ctx context.Context, name, after string, limit int) ([]library.Child, error) {
	p.calls.Add(1)
	return p.fixture.ReadDir(ctx, name, after, limit)
}

func (p *contractProvider) OpenVersion(ctx context.Context, name, revision string) (media.Representation, error) {
	p.calls.Add(1)
	p.opens.Add(1)

	rep, err := p.fixture.OpenVersion(ctx, name, revision)

	return contractBytes{Representation: rep, provider: p}, err
}

type contractBytes struct {
	media.Representation
	provider *contractProvider
}

func (r contractBytes) ReadAt(ctx context.Context, p []byte, offset int64) (int, error) {
	r.provider.calls.Add(1)

	if r.provider.gate != nil {
		select {
		case r.provider.entered <- struct{}{}:
		case <-ctx.Done():
			return 0, ctx.Err()
		}

		select {
		case <-r.provider.gate:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}

	n, err := r.Representation.ReadAt(ctx, p, offset)
	r.provider.bytes.Add(int64(n))

	return n, err
}

func rpcFixture(t testing.TB) (*Server, *contractProvider, []record) {
	t.Helper()

	h, err := openHandles(t.Context(), filepath.Join(t.TempDir(), "nfs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := h.db.Close(); err != nil {
			t.Error(err)
		}
	})

	p := &contractProvider{fixture: fixture{count: 1}}
	s := &Server{handles: h, provider: p, reads: make(chan struct{}, maxReads)}

	var records []record

	for _, name := range []string{"", "Recording-00000.bin"} {
		r, err := s.describe(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}

		records = append(records, r)
	}

	if err = h.save(t.Context(), records); err != nil {
		t.Fatal(err)
	}

	return s, p, records
}

func rpcRequest(xid, program, procedure uint32, args encoder) encoder {
	var request encoder
	for _, value := range []uint32{xid, 0, 2, program, 3, procedure, 0, 0, 0, 0} {
		request.u32(value)
	}

	return append(request, args...)
}

func nfsArguments(procedure uint32, handle []byte) encoder {
	var args encoder
	if procedure != 0 {
		args.opaque(handle)
	}

	switch procedure {
	case 3:
		args.opaque([]byte("Recording-00000.bin"))
	case 4:
		args.u32(63)
	case 6:
		args.u64(0)
		args.u32(16)
	case 16, 17:
		args.u64(0)
		args.u64(0)
		args.u32(32768)

		if procedure == 17 {
			args.u32(32768)
		}
	}

	return args
}

func TestRPCRejectsMalformedArgumentsBeforeProviderAccess(t *testing.T) {
	s, p, records := rpcFixture(t)
	for _, procedure := range []uint32{0, 1, 3, 4, 5, 6, 16, 17, 18, 19, 20} {
		for _, r := range records {
			request := rpcRequest(1, 100003, procedure, nfsArguments(procedure, r.id))
			for _, malformed := range [][]byte{request[:len(request)-1], append(append([]byte(nil), request...), 0, 0, 0, 0)} {
				before := p.calls.Load()

				reply := s.rpc(t.Context(), malformed)
				if code := binary.BigEndian.Uint32(reply[20:24]); code != 4 || p.calls.Load() != before {
					t.Fatal("malformed RPC reached provider or changed rejection", procedure, code, p.calls.Load()-before)
				}
			}
		}
	}

	for _, procedure := range []uint32{0, 1, 2, 3, 4, 5} {
		var args encoder
		if procedure == 1 || procedure == 3 {
			args.opaque([]byte("/media"))
		}

		before := p.calls.Load()

		request := append(rpcRequest(1, 100005, procedure, args), 0)

		reply := s.rpc(t.Context(), request)
		if code := binary.BigEndian.Uint32(reply[20:24]); code != 4 || p.calls.Load() != before {
			t.Fatal("malformed mount RPC reached provider", procedure, code)
		}
	}
}

func TestRPCFileReadSeedReachesPayload(t *testing.T) {
	s, p, records := rpcFixture(t)
	request := rpcRequest(1, 100003, 6, nfsArguments(6, records[1].id))

	reply := s.rpc(t.Context(), request)
	if binary.BigEndian.Uint32(reply[20:24]) != 0 || binary.BigEndian.Uint32(reply[24:28]) != 0 || p.opens.Load() != 1 || p.bytes.Load() != 16 {
		t.Fatal("file seed did not reach a successful payload read", p.opens.Load(), p.bytes.Load())
	}
}

func TestReadAdmissionReleasesCapacityAfterCancellation(t *testing.T) {
	s, p, records := rpcFixture(t)
	p.entered = make(chan struct{}, 1)
	p.gate = make(chan struct{})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	request := rpcRequest(1, 100003, 6, nfsArguments(6, records[1].id))

	done := make(chan encoder, 1)
	go func() { done <- s.rpc(ctx, request) }()

	select {
	case <-p.entered:
	case <-t.Context().Done():
		t.Fatal("READ did not reach provider")
	}

	cancel()
	<-done

	if len(s.reads) != 0 {
		t.Fatal("canceled READ retained capacity", len(s.reads))
	}

	p.gate = nil

	reply := s.rpc(t.Context(), request)
	if binary.BigEndian.Uint32(reply[24:28]) != 0 {
		t.Fatal("READ did not recover after cancellation")
	}
}
