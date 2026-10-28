//go:build linux

package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"syscall"
	"time"
)

const (
	helperSocket = "/run/duplex/mount.sock"
	helperMount  = "/library/media"
	serviceUID   = 10001
)

// ServeMountHelper grants one fixed read-only mount to the service UID. It never
// reads configuration or accepts paths, mount options, or executable names.
func ServeMountHelper(ctx context.Context) error {
	return serveMountHelper(ctx, helperSocket, helperMount, serviceUID)
}

func serveMountHelper(ctx context.Context, socket, mountpoint string, uid uint32) error {
	if err := removeHelperSocket(ctx, socket); err != nil {
		return err
	}

	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: socket, Net: "unixpacket"})
	if err != nil {
		return err
	}
	defer closeMountControl(listener)

	if err = os.Chmod(socket, 0o666); err != nil { //nolint:gosec // G302: The read-only client bind and SO_PEERCRED check protect this socket.
		return err
	}

	stop := context.AfterFunc(ctx, func() { closeMountControl(listener) })
	defer stop()

	for {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			if ctx.Err() != nil {
				return nil
			}

			return acceptErr
		}

		if err = serveMountLease(ctx, connection, mountpoint, uid); err != nil {
			slog.Debug("mount helper session ended", "error", err)
		}
	}
}

func removeHelperSocket(ctx context.Context, socket string) error {
	info, err := os.Lstat(socket)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSocket == 0 || !ok || stat.Uid != 0 {
		return errors.New("mount helper socket path is not an owned socket")
	}

	connection, dialErr := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unixpacket", socket)
	if dialErr == nil {
		return errors.Join(errors.New("mount helper is already running"), connection.Close())
	}

	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return dialErr
	}

	return os.Remove(socket)
}

func peerUID(connection *net.UnixConn) (uint32, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}

	var (
		credentials   *syscall.Ucred
		credentialErr error
	)

	err = raw.Control(func(fd uintptr) {
		credentials, credentialErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err = errors.Join(err, credentialErr); err != nil {
		return 0, err
	}

	return credentials.Uid, nil
}

func serveMountLease(ctx context.Context, connection *net.UnixConn, mountpoint string, uid uint32) error {
	defer closeMountControl(connection)

	stop := context.AfterFunc(ctx, func() { closeMountControl(connection) })
	defer stop()

	if err := authorizeMount(connection, uid); err != nil {
		return err
	}

	device, err := openHelperMount(mountpoint, uid)
	if err != nil {
		_, writeErr := connection.Write([]byte("E"))
		return errors.Join(err, writeErr)
	}

	_, _, err = connection.WriteMsgUnix([]byte("F"), syscall.UnixRights(int(device.Fd())), nil)

	err = errors.Join(err, device.Close())
	if err == nil {
		err = connection.SetDeadline(time.Time{})
	}

	if err == nil {
		var release [1]byte

		_, err = connection.Read(release[:])
		if errors.Is(err, io.EOF) {
			err = nil
		}
	}

	response := []byte("0")

	unmountErr := closeHelperMount(mountpoint)
	if unmountErr != nil {
		response[0] = 'E'
	}

	_, writeErr := connection.Write(response)

	return errors.Join(err, unmountErr, writeErr)
}

func closeMountControl(control io.Closer) {
	if err := control.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.Debug("mount helper control cleanup failed", "error", err)
	}
}

func authorizeMount(connection *net.UnixConn, uid uint32) error {
	peer, err := peerUID(connection)
	if err != nil || peer != uid {
		return errors.New("mount helper peer is not the service user")
	}

	if err = connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}

	var request [2]byte

	n, err := connection.Read(request[:])
	if err != nil || n != 1 || request[0] != 'M' {
		return errors.New("invalid mount helper request")
	}

	return nil
}

func openHelperMount(mountpoint string, uid uint32) (*os.File, error) {
	if err := prepareMount(mountpoint); err != nil {
		return nil, err
	}

	device, err := os.OpenFile("/dev/fuse", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}

	options := fmt.Sprintf("fd=%d,rootmode=40000,user_id=%d,group_id=%d,allow_other,default_permissions", device.Fd(), uid, uid)

	flags := uintptr(syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC)
	if err = syscall.Mount("duplex", mountpoint, "fuse.duplex", flags, options); err != nil {
		return nil, errors.Join(err, device.Close())
	}

	return device, nil
}

func closeHelperMount(mountpoint string) error {
	err := syscall.Unmount(mountpoint, 0)
	if errors.Is(err, syscall.EBUSY) {
		// Remove the public path even if an external reader retains a handle.
		// Return the busy error so the service exits instead of waiting forever.
		return errors.Join(err, syscall.Unmount(mountpoint, syscall.MNT_DETACH))
	}

	return err
}

// helperResponse reads one complete packet. Ancillary descriptors are only
// accepted by receiveMountFD, never by this lifecycle channel.
func helperResponse(connection *net.UnixConn) error {
	var response [2]byte

	n, err := connection.Read(response[:])
	if err != nil {
		return err
	}

	if n != 1 || response[0] != '0' {
		return errors.New("mount helper could not release the mount")
	}

	return nil
}
