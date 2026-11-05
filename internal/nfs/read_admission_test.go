package nfs

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"
)

func sendRPC(t *testing.T, connection net.Conn, request encoder) {
	t.Helper()

	frame := binary.BigEndian.AppendUint32(nil, size32(len(request))|0x80000000)
	if _, err := connection.Write(append(frame, request...)); err != nil {
		t.Fatal(err)
	}
}

func readRPC(t *testing.T, connection net.Conn) (uint32, uint32) {
	t.Helper()

	if err := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	reply, err := readRecord(connection)
	if err != nil || len(reply) < 28 {
		t.Fatal("missing NFS response", err)
	}

	return binary.BigEndian.Uint32(reply[:4]), binary.BigEndian.Uint32(reply[24:28])
}

func TestReadAdmissionKeepsMetadataAvailableAtCapacity(t *testing.T) {
	gate := make(chan struct{})
	p := &contractProvider{fixture: fixture{count: 1}, entered: make(chan struct{}, maxReads), gate: gate}

	s, err := Listen(t.Context(), "127.0.0.1:0", t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	var once sync.Once

	unblock := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(unblock)

	root := rootHandle(t, s)
	handle := fileHandle(t, s, root)
	dial := func() net.Conn {
		connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", s.Addr().String())
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { closePeer(connection) })

		return connection
	}

	connection := dial()

	readArgs := nfsArguments(6, handle)
	for i := range maxReads {
		sendRPC(t, connection, rpcRequest(uint32(i+1), 100003, 6, readArgs))
	}

	for range maxReads {
		select {
		case <-p.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("payload reads did not reach capacity")
		}
	}

	for i := range 8 {
		sendRPC(t, connection, rpcRequest(uint32(100+i), 100003, 6, readArgs))
	}

	// A metadata request on this same connection must pass the queued READs.
	for i, procedure := range []uint32{1, 16, 17} {
		sendRPC(t, connection, rpcRequest(uint32(200+i), 100003, procedure, nfsArguments(procedure, root)))
	}

	seen := map[uint32]bool{}

	for range 11 {
		xid, code := readRPC(t, connection)

		want := uint32(10008)
		if xid >= 200 && xid <= 202 {
			want = 0
		} else if xid < 100 || xid > 107 {
			t.Fatal("a blocked read completed before release", xid)
		}

		if code != want || seen[xid] {
			t.Fatal("missing metadata or retryable overload response", xid, code)
		}

		seen[xid] = true
	}

	other := dial()
	for _, procedure := range []uint32{1, 16, 17} {
		sendRPC(t, other, rpcRequest(procedure, 100003, procedure, nfsArguments(procedure, root)))

		if _, code := readRPC(t, other); code != 0 {
			t.Fatal("metadata on another connection was blocked", procedure, code)
		}
	}

	if p.opens.Load() != maxReads || len(s.reads) != maxReads {
		t.Fatal("overflow READ reached the provider", p.opens.Load(), len(s.reads))
	}

	unblock()

	for range maxReads {
		if _, code := readRPC(t, connection); code != 0 {
			t.Fatal("admitted read failed after release", code)
		}
	}

	sendRPC(t, other, rpcRequest(300, 100003, 6, readArgs))

	if _, code := readRPC(t, other); code != 0 {
		t.Fatal("read capacity did not recover", code)
	}
}
