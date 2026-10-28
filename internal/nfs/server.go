package nfs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"duplex/internal/library"
)

const (
	maxRequest = 1 << 20
	maxRead    = 128 << 10
)

// Server owns its TCP listener, bounded request workers, and durable file handles.
// Only the projected library is exported. AUTH_SYS is not user authentication;
// deployments must restrict the listener to the trusted Docker host/network.
type Server struct {
	provider library.VersionedProvider
	handles  *handles
	listener net.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	workers  sync.WaitGroup
	mu       sync.Mutex
	peers    map[net.Conn]bool
	slots    chan struct{}
	ready    atomic.Bool
}

// Listen starts an unprivileged NFSv3 server. The state directory must be private.
func Listen(ctx context.Context, address, directory string, provider library.VersionedProvider) (*Server, error) {
	h, err := openHandles(ctx, filepath.Join(directory, "nfs.sqlite"))
	if err != nil {
		return nil, err
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return nil, errors.Join(err, h.db.Close())
	}

	ctx, cancel := context.WithCancel(ctx)
	s := &Server{provider: provider, handles: h, listener: listener, ctx: ctx, cancel: cancel, done: make(chan struct{}), peers: map[net.Conn]bool{}, slots: make(chan struct{}, 32)}

	s.ready.Store(true)
	go s.accept(ctx)

	return s, nil
}

// Addr reports the bound address for controlled tests and deployment checks.
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Ready reports whether the export accepts new connections.
func (s *Server) Ready() bool { return s.ready.Load() }

// Done closes if the export listener stops.
func (s *Server) Done() <-chan struct{} { return s.done }

// Close cancels requests and closes connections before releasing wire identities.
func (s *Server) Close() error {
	s.cancel()
	err := s.listener.Close()
	<-s.done
	s.mu.Lock()
	for connection := range s.peers {
		closePeer(connection)
	}

	s.mu.Unlock()
	s.workers.Wait()

	return errors.Join(err, s.handles.db.Close())
}

func (s *Server) accept(ctx context.Context) {
	defer close(s.done)
	defer s.ready.Store(false)

	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}

		s.mu.Lock()
		if len(s.peers) >= 64 {
			closePeer(connection)
			s.mu.Unlock()

			continue
		}

		s.peers[connection] = true
		s.workers.Add(1)

		s.mu.Unlock()
		go s.serve(ctx, connection)
	}
}

func readRecord(connection net.Conn) ([]byte, error) {
	var data []byte

	for range 64 {
		header := make([]byte, 4)
		if _, err := io.ReadFull(connection, header); err != nil {
			return nil, err
		}

		marker := binary.BigEndian.Uint32(header)

		count := int(marker & 0x7fffffff)
		if count > maxRequest-len(data) {
			return nil, errEncoding
		}

		start := len(data)

		data = append(data, make([]byte, count)...)
		if _, err := io.ReadFull(connection, data[start:]); err != nil {
			return nil, err
		}

		if marker&0x80000000 != 0 {
			return data, nil
		}
	}

	return nil, errEncoding
}

func (s *Server) serve(ctx context.Context, connection net.Conn) {
	defer s.workers.Done()
	defer func() {
		closePeer(connection)
		s.mu.Lock()
		delete(s.peers, connection)
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(ctx)

	var (
		requests sync.WaitGroup
		writes   sync.Mutex
	)

	defer requests.Wait()
	defer cancel()

	for s.ctx.Err() == nil {
		if err := connection.SetReadDeadline(time.Now().Add(5 * time.Minute)); err != nil {
			return
		}

		request, err := readRecord(connection)
		if err != nil {
			return
		}

		select {
		case s.slots <- struct{}{}:
		case <-ctx.Done():
			return
		}

		requests.Add(1)
		go func() {
			defer requests.Done()
			defer func() { <-s.slots }()

			if err := s.respond(ctx, connection, &writes, request); err != nil {
				cancel()
				closePeer(connection)
			}
		}()
	}
}

func (s *Server) respond(ctx context.Context, connection net.Conn, writes *sync.Mutex, request []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	response := s.rpc(ctx, request)
	data := binary.BigEndian.AppendUint32(nil, size32(len(response))|0x80000000)
	data = append(data, response...)

	writes.Lock()
	defer writes.Unlock()

	if err := connection.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}

	_, err := io.Copy(connection, bytes.NewReader(data))

	return err
}

func closePeer(connection net.Conn) {
	if err := connection.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.Debug("NFS client disconnected during close")
	}
}
