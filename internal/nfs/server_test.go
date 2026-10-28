package nfs

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"duplex/internal/library"
	"duplex/internal/media"
)

type fixture struct {
	count      int
	version    atomic.Int64
	fail       atomic.Bool
	maxPage    atomic.Int64
	pageWork   atomic.Int64
	membership atomic.Int64
}

func (p *fixture) Stat(ctx context.Context, name string) (library.Node, error) {
	if err := ctx.Err(); err != nil {
		return library.Node{}, err
	}

	if name == "" {
		return library.Node{Directory: true, Modified: 123, Revision: fmt.Sprint(p.membership.Load())}, nil
	}

	if p.membership.Load() < 0 {
		return library.Node{}, fs.ErrNotExist
	}

	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "Recording-"), ".bin"))
	if err != nil || n < 0 || n >= p.count || name != fmt.Sprintf("Recording-%05d.bin", n) {
		return library.Node{}, fs.ErrNotExist
	}

	return library.Node{Size: 256 << 20, Modified: 123, Revision: fmt.Sprint(p.version.Load())}, nil
}

func (p *fixture) Open(ctx context.Context, name string) (media.Representation, int64, error) {
	info, err := p.Stat(ctx, name)
	return fixtureBytes{version: info.Revision, fail: &p.fail}, info.Modified, err
}

func (p *fixture) OpenVersion(ctx context.Context, name, revision string) (media.Representation, error) {
	rep, _, err := p.Open(ctx, name)
	if err == nil && rep.ID() != revision {
		return nil, media.ErrChanged
	}

	return rep, err
}

func (p *fixture) ReadDir(ctx context.Context, name, after string, limit int) ([]library.Child, error) {
	if name != "" || limit < 1 || limit > 256 {
		return nil, fs.ErrInvalid
	}

	if p.membership.Load() < 0 {
		return nil, ctx.Err()
	}

	p.maxPage.Store(max(p.maxPage.Load(), int64(limit)))

	start := 0

	if after != "" {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(after, "Recording-"), ".bin"))
		if err != nil {
			return nil, err
		}

		start = n + 1
	}

	var children []library.Child
	for i := start; i < min(start+limit, p.count); i++ {
		children = append(children, library.Child{Name: fmt.Sprintf("Recording-%05d.bin", i)})

		p.pageWork.Add(1)
	}

	return children, ctx.Err()
}

type fixtureBytes struct {
	version string
	fail    *atomic.Bool
}

func (r fixtureBytes) ID() string  { return r.version }
func (r fixtureBytes) Size() int64 { return 256 << 20 }

func (r fixtureBytes) ReadAt(ctx context.Context, output []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	if r.fail.Load() {
		return 0, io.ErrUnexpectedEOF
	}

	for i := range output {
		output[i] = byte((offset + int64(i)) & 255)
	}

	return len(output), nil
}

func call(t *testing.T, s *Server, program, procedure uint32, args encoder) *decoder {
	t.Helper()

	var request encoder
	for _, v := range []uint32{1234, 0, 2, program, 3, procedure, 0, 0, 0, 0} {
		request.u32(v)
	}

	d := &decoder{data: s.rpc(t.Context(), append(request, args...))}
	for _, want := range []uint32{1234, 1, 0, 0, 0, 0} {
		if got := d.u32(); got != want {
			t.Fatalf("RPC header %d, want %d", got, want)
		}
	}

	return d
}

func start(t *testing.T, state string, p *fixture) *Server {
	t.Helper()

	s, err := Listen(t.Context(), "127.0.0.1:0", state, p)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if s.Ready() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}
	})

	return s
}

func rootHandle(t *testing.T, s *Server) []byte {
	t.Helper()

	var args encoder
	args.opaque([]byte("/media"))

	d := call(t, s, 100005, 1, args)
	if got := d.u32(); got != 0 {
		t.Fatalf("mount status %d", got)
	}

	return d.opaque(64)
}

func TestDirectoryMembershipAdvancesTimeWithStableHandleAcrossRestart(t *testing.T) {
	p := &fixture{count: 1}
	state := t.TempDir()
	s := start(t, state, p)
	root := rootHandle(t, s)

	for change := int64(0); change < 3; change++ {
		p.membership.Store(change % 2) // Return to an earlier membership in the same second.

		var args encoder
		args.opaque(root)

		d := call(t, s, 100003, 1, args)
		if d.u32() != 0 {
			t.Fatal("directory handle became stale")
		}

		d.take(68)

		if modified := d.u32(); modified != uint32(123+change) {
			t.Fatalf("mtime %d does not identify membership change %d", modified, change)
		}

		if handle := rootHandle(t, s); string(handle) != string(root) {
			t.Fatal("membership change replaced the root handle")
		}

		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		s = start(t, state, p)
	}
}

func fileHandle(t *testing.T, s *Server, root []byte) []byte {
	t.Helper()

	var args encoder
	args.opaque(root)
	args.opaque([]byte("Recording-00000.bin"))

	d := call(t, s, 100003, 3, args)
	if got := d.u32(); got != 0 {
		t.Fatalf("lookup status %d", got)
	}

	return d.opaque(64)
}

func TestWireReadsIdentityAndRestart(t *testing.T) {
	p := &fixture{count: 10000}
	state := t.TempDir()
	s := start(t, state, p)
	root := rootHandle(t, s)
	handle := fileHandle(t, s, root)

	var args encoder
	args.opaque(handle)
	args.u64(1 << 20)
	args.u32(4096)

	d := call(t, s, 100003, 6, args)
	if d.u32() != 0 || d.u32() != 1 {
		t.Fatal("read failed")
	}

	d.take(84)

	if d.u32() != 4096 || d.u32() != 0 {
		t.Fatal("wrong read size or EOF")
	}

	value := d.opaque(maxRead)
	if len(value) != 4096 || value[255] != 255 {
		t.Fatal("wrong byte mapping")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = start(t, state, p)
	if call(t, s, 100003, 6, args).u32() != 0 {
		t.Fatal("restart invalidated an unchanged handle")
	}

	p.version.Store(1)

	if call(t, s, 100003, 6, args).u32() != 70 {
		t.Fatal("an old handle returned replacement bytes")
	}

	newHandle := fileHandle(t, s, root)
	if string(newHandle) == string(handle) {
		t.Fatal("replacement kept the old handle")
	}

	p.fail.Store(true)

	args = nil
	args.opaque(newHandle)
	args.u64(0)
	args.u32(4096)

	if call(t, s, 100003, 6, args).u32() != 5 {
		t.Fatal("source failure did not reach NFS")
	}
}

func TestDirectoryPagingIsBoundedAndComplete(t *testing.T) {
	p := &fixture{count: 10000}
	s := start(t, t.TempDir(), p)
	root := rootHandle(t, s)

	var cursor, verifier uint64

	count := 0

	for {
		var args encoder
		args.opaque(root)
		args.u64(cursor)
		args.u64(verifier)
		args.u32(32768)
		args.u32(32768)

		d := call(t, s, 100003, 17, args)
		if d.u32() != 0 || d.u32() != 1 {
			t.Fatal("directory request failed")
		}

		d.take(84)

		verifier = d.u64()
		for d.u32() != 0 {
			d.u64()
			name := string(d.opaque(255))
			cursor = d.u64()

			if name != fmt.Sprintf("Recording-%05d.bin", count) {
				t.Fatalf("out of order: %s at %d", name, count)
			}

			count++

			if d.u32() != 1 {
				t.Fatal("missing attributes")
			}

			d.take(84)

			if d.u32() != 1 || len(d.opaque(64)) != 32 {
				t.Fatal("missing handle")
			}
		}

		if d.u32() != 0 {
			break
		}
	}

	if count != p.count || p.maxPage.Load() > 129 || p.pageWork.Load() > 10200 {
		t.Fatalf("unbounded/incomplete traversal: %d, max page %d, work %d", count, p.maxPage.Load(), p.pageWork.Load())
	}
}

func TestReadonlyFailuresAndTCP(t *testing.T) {
	s := start(t, t.TempDir(), &fixture{count: 1})

	root := rootHandle(t, s)
	for _, procedure := range []uint32{2, 7, 8, 9, 10, 11, 12, 13, 14, 15, 21} {
		if got := call(t, s, 100003, procedure, nil).u32(); got != 30 {
			t.Fatalf("mutation %d: %d", procedure, got)
		}
	}

	var args encoder
	args.opaque(root)

	for _, procedure := range []uint32{1, 18, 19, 20} {
		if call(t, s, 100003, procedure, args).u32() != 0 {
			t.Fatal("attribute operation failed", procedure)
		}
	}

	connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer closePeer(connection)

	var request encoder
	for _, v := range []uint32{9, 0, 2, 100003, 3, 0, 0, 0, 0, 0} {
		request.u32(v)
	}

	data := binary.BigEndian.AppendUint32(nil, size32(len(request))|0x80000000)
	if _, err = connection.Write(append(data, request...)); err != nil {
		t.Fatal(err)
	}

	if response, err := readRecord(connection); err != nil || len(response) != 24 {
		t.Fatal("TCP RPC failed", response, err)
	}
}

func FuzzRequest(f *testing.F) {
	h, err := openHandles(f.Context(), f.TempDir()+"/nfs.sqlite")
	if err != nil {
		f.Fatal(err)
	}

	f.Cleanup(func() {
		if err := h.db.Close(); err != nil {
			f.Error(err)
		}
	})

	s := &Server{handles: h, provider: &fixture{count: 1}}

	f.Add([]byte("invalid request"))

	for _, procedure := range []uint32{0, 1, 3, 4, 6, 16, 17, 18, 19, 20, 21} {
		var request encoder
		for _, value := range []uint32{1, 0, 2, 100003, 3, procedure, 0, 0, 0, 0} {
			request.u32(value)
		}

		root, err := s.describe(f.Context(), "")
		if err != nil {
			f.Fatal(err)
		}

		if err = h.save(f.Context(), []record{root}); err != nil {
			f.Fatal(err)
		}

		request.opaque(root.id)
		request.u64(0)
		request.u64(0)
		request.u32(32768)
		request.u32(32768)
		f.Add([]byte(request))
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) <= maxRequest {
			_ = s.rpc(t.Context(), input)
		}
	})
}

func TestReadErrorsAndDirectoryCursors(t *testing.T) {
	s := start(t, t.TempDir(), &fixture{count: 3})
	root := rootHandle(t, s)
	handle := fileHandle(t, s, root)

	for _, name := range []string{"../secret", "missing", "", "bad\x00name"} {
		var args encoder
		args.opaque(root)
		args.opaque([]byte(name))

		if call(t, s, 100003, 3, args).u32() == 0 {
			t.Fatal("invalid lookup succeeded", name)
		}
	}

	for _, plus := range []bool{false, true} {
		var args encoder
		args.opaque(root)
		args.u64(0)
		args.u64(0)
		args.u32(32768)

		procedure := uint32(16)
		if plus {
			procedure = 17

			args.u32(32768)
		}

		if call(t, s, 100003, procedure, args).u32() != 0 {
			t.Fatal("directory variant failed", procedure)
		}

		args = nil
		args.opaque(root)
		args.u64(999)
		args.u64(123)
		args.u32(32768)

		if plus {
			args.u32(32768)
		}

		if call(t, s, 100003, procedure, args).u32() != 10003 {
			t.Fatal("unknown directory cursor was accepted")
		}
	}

	var accessArgs encoder
	accessArgs.opaque(root)
	accessArgs.u32(63)

	response := call(t, s, 100003, 4, accessArgs)
	if response.u32() != 0 || response.u32() != 1 {
		t.Fatal("access failed")
	}

	response.take(84)

	if response.u32() != 35 {
		t.Fatal("access grants writes")
	}

	for _, offset := range []uint64{256 << 20, 256<<20 - 1, 1 << 63} {
		var args encoder
		args.opaque(handle)
		args.u64(offset)
		args.u32(4096)

		want := uint32(0)
		if offset == 1<<63 {
			want = 22
		}

		if call(t, s, 100003, 6, args).u32() != want {
			t.Fatal("offset bounds failed", offset)
		}
	}
}

func TestKernelFixture(t *testing.T) {
	if os.Getenv("DUPLEX_NFS_FIXTURE") != "1" {
		t.Skip("dedicated NFS kernel-client fixture")
	}

	ctx, cancel := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	p := &fixture{count: 10000}

	s, err := Listen(ctx, "0.0.0.0:2049", "/state", p)
	if err != nil {
		t.Fatal(err)
	}

	t.Log("NFS fixture ready")

	changes := make(chan os.Signal, 1)

	signal.Notify(changes, syscall.SIGUSR1)
	defer signal.Stop(changes)

	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-changes:
			p.membership.Store(^p.membership.Load())
		}
	}

	if err = s.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
