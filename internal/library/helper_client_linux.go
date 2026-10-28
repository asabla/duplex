//go:build linux

package library

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type mountLease struct {
	connection *net.UnixConn
	done       chan struct{}
	once       sync.Once
	err        error // Written before done is closed; read only after done.
	closeErr   error // Protected by once.
}

func acquireMount(socket string) (*mountLease, int, error) {
	connection, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: socket, Net: "unixpacket"})
	if err != nil {
		return nil, -1, err
	}

	uid, err := peerUID(connection)
	if err != nil || uid != 0 {
		return nil, -1, errors.Join(errors.New("mount helper must run as root"), connection.Close())
	}

	if err = connection.SetDeadline(time.Now().Add(5 * time.Second)); err == nil {
		_, err = connection.Write([]byte("M"))
	}

	fd := -1
	if err == nil {
		fd, err = receiveMountFD(connection)
	}

	if err != nil {
		return nil, -1, errors.Join(err, connection.Close())
	}

	if err = connection.SetDeadline(time.Time{}); err != nil {
		return nil, -1, errors.Join(err, syscall.Close(fd), connection.Close())
	}

	lease := &mountLease{connection: connection, done: make(chan struct{})}
	go func() {
		lease.err = helperResponse(connection)
		close(lease.done)
	}()

	return lease, fd, nil
}

func receiveMountFD(connection *net.UnixConn) (int, error) {
	var response [2]byte

	control := make([]byte, syscall.CmsgSpace(16*4))

	n, count, flags, _, err := connection.ReadMsgUnix(response[:], control)
	if err != nil {
		return -1, err
	}

	messages, err := syscall.ParseSocketControlMessage(control[:count])

	var descriptors []int

	for _, message := range messages {
		var received []int

		received, err = syscall.ParseUnixRights(&message)
		if err != nil {
			break
		}

		descriptors = append(descriptors, received...)
	}

	if err != nil || n != 1 || response[0] != 'F' || len(descriptors) != 1 || flags&(syscall.MSG_TRUNC|syscall.MSG_CTRUNC) != 0 {
		for _, fd := range descriptors {
			err = errors.Join(err, syscall.Close(fd))
		}

		return -1, errors.Join(errors.New("invalid mount helper descriptor response"), err)
	}

	syscall.CloseOnExec(descriptors[0])

	return descriptors[0], nil
}

func (l *mountLease) Close() error {
	l.once.Do(func() {
		l.closeErr = l.connection.SetDeadline(time.Now().Add(5 * time.Second))
		if l.closeErr == nil {
			_, l.closeErr = l.connection.Write([]byte("R"))
		}

		if l.closeErr != nil {
			closeMountControl(l.connection)
		}

		<-l.done
		closeMountControl(l.connection)
	})

	return errors.Join(l.err, l.closeErr)
}

// MountProviderWithHelper serves an existing FUSE descriptor without mount
// capabilities or access to /dev/fuse in this process.
func MountProviderWithHelper(mountpoint string, provider Provider, socket string) (*Mount, error) {
	if provider == nil {
		return nil, errors.New("library provider is required")
	}

	lease, fd, err := acquireMount(socket)
	if err != nil {
		return nil, err
	}

	m := &Mount{path: mountpoint, provider: provider, lease: lease}
	timeout := time.Duration(0)
	options := &fs.Options{AttrTimeout: &timeout, EntryTimeout: &timeout, MountOptions: fuse.MountOptions{FsName: "duplex", Name: "duplex", DisableXAttrs: true}, RootStableAttr: &fs.StableAttr{Ino: Inode("duplex-root")}}

	m.server, err = fs.Mount(fmt.Sprintf("/dev/fd/%d", fd), &node{mount: m}, options)
	if err != nil {
		return nil, errors.Join(err, lease.Close())
	}

	if !m.Ready() {
		return nil, errors.Join(errors.New("mount helper projection is not visible"), m.Close())
	}

	return m, nil
}
