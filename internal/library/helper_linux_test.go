//go:build linux

package library

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func helperPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()

	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		t.Fatal(err)
	}

	connections := make([]*net.UnixConn, 2)

	for i, fd := range pair {
		file := os.NewFile(uintptr(fd), "helper-fixture")
		connection, openErr := net.FileConn(file)
		closeTest(t, file)

		if openErr != nil {
			t.Fatal(openErr)
		}

		var ok bool

		connections[i], ok = connection.(*net.UnixConn)
		if !ok {
			t.Fatal("socket pair is not Unix")
		}

		t.Cleanup(func() { closeMountControl(connection) })
	}

	return connections[0], connections[1]
}

func TestHelperDescriptorProtocol(t *testing.T) {
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, file)

	for _, scenario := range []struct {
		name, payload string
		count         int
		valid         bool
	}{
		{"valid", "F", 1, true},
		{"no descriptor", "F", 0, false},
		{"wrong response", "E", 1, false},
		{"extra payload", "FX", 1, false},
		{"multiple descriptors", "F", 2, false},
		{"truncated rights", "F", 32, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			client, server := helperPair(t)

			descriptors := make([]int, scenario.count)
			for i := range descriptors {
				descriptors[i] = int(file.Fd())
			}

			before, readErr := os.ReadDir("/proc/self/fd")
			if readErr != nil {
				t.Fatal(readErr)
			}

			if _, _, sendErr := server.WriteMsgUnix([]byte(scenario.payload), syscall.UnixRights(descriptors...), nil); sendErr != nil {
				t.Fatal(sendErr)
			}

			fd, receiveErr := receiveMountFD(client)
			if (receiveErr == nil) != scenario.valid {
				t.Fatal(fd, receiveErr)
			}

			if scenario.valid {
				flags, _, flagErr := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
				if flagErr != 0 || flags&syscall.FD_CLOEXEC == 0 {
					t.Fatal("descriptor can leak to subprocesses", flags, flagErr)
				}

				if closeErr := syscall.Close(fd); closeErr != nil {
					t.Fatal(closeErr)
				}
			}

			after, readErr := os.ReadDir("/proc/self/fd")
			if readErr != nil || len(before) != len(after) {
				t.Fatal("descriptor leak", len(before), len(after), readErr)
			}
		})
	}
}

func TestHelperAuthorizationAndReleaseResponses(t *testing.T) {
	for _, request := range []string{"M", "", "MM", "/tmp"} {
		client, server := helperPair(t)
		if _, err := client.Write([]byte(request)); err != nil {
			t.Fatal(err)
		}

		err := authorizeMount(server, uint32(os.Getuid())) //nolint:gosec // G115: Linux user IDs fit in uint32.
		if (err == nil) != (request == "M") {
			t.Fatal(request, err)
		}
	}

	client, server := helperPair(t)
	if err := authorizeMount(server, uint32(os.Getuid())+1); err == nil { //nolint:gosec // G115: The test chooses a distinct UID.
		t.Fatal("untrusted peer was authorized")
	}

	for _, response := range []string{"0", "E", "00"} {
		if _, err := client.Write([]byte(response)); err != nil {
			t.Fatal(err)
		}

		if err := helperResponse(server); (err == nil) != (response == "0") {
			t.Fatal(response, err)
		}
	}

	closeTest(t, client)

	if _, err := receiveMountFD(server); err == nil {
		t.Fatal("closed connection supplied a descriptor")
	}

	if err := helperResponse(server); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

func startFixtureHelper(t *testing.T, mountpoint string, uid uint32) (string, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	socket := filepath.Join(t.TempDir(), "mount.sock")

	done := make(chan error, 1)
	go func() { done <- serveMountHelper(ctx, socket, mountpoint, uid) }()

	t.Cleanup(func() {
		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("mount helper did not stop")
		}
	})

	for deadline := time.Now().Add(3 * time.Second); ; {
		if _, err := os.Stat(socket); err == nil {
			return socket, cancel
		}

		if time.Now().After(deadline) {
			t.Fatal("mount helper did not listen")
		}

		time.Sleep(time.Millisecond)
	}
}

func TestHelperSocketOwnershipAndFailures(t *testing.T) {
	root := t.TempDir()

	file := filepath.Join(root, "socket")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := removeHelperSocket(t.Context(), file); err == nil {
		t.Fatal("ordinary file was removed")
	}

	if err := serveMountHelper(t.Context(), file, root, 0); err == nil {
		t.Fatal("helper replaced ordinary file")
	}

	if err := serveMountHelper(t.Context(), filepath.Join(root, "missing", "socket"), root, 0); err == nil {
		t.Fatal("helper listened without parent directory")
	}

	socket, _ := startFixtureHelper(t, filepath.Join(root, "unusable-mount"), uint32(os.Getuid())) //nolint:gosec // G115: Linux UID.
	if err := removeHelperSocket(t.Context(), socket); err == nil {
		t.Fatal("live or unowned socket was removed")
	}

	if _, _, err := acquireMount(filepath.Join(root, "absent")); err == nil {
		t.Fatal("missing helper supplied a mount")
	}

	if _, err := MountProviderWithHelper(root, nil, socket); err == nil {
		t.Fatal("nil provider accepted")
	}

	// Fail before opening /dev/fuse even on root FUSE qualification runs.
	if err := os.WriteFile(filepath.Join(root, "unusable-mount"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := acquireMount(socket); err == nil {
		t.Fatal("helper with invalid destination supplied a mount")
	}
}

func TestFuseHelperReadOnlyRestartAndLoss(t *testing.T) {
	if os.Getenv("DUPLEX_FUSE_TEST") != "1" {
		t.Skip("requires Linux FUSE")
	}

	root := t.TempDir()
	mountpoint := filepath.Join(root, "media")
	socket, stop := startFixtureHelper(t, mountpoint, 0)

	provider := &fixtureProvider{files: map[string]blob{"Channel/episode.mp4": {id: "stable", data: []byte("controlled media")}}}
	for range 2 {
		mount, err := MountProviderWithHelper(mountpoint, provider, socket)
		if err != nil {
			t.Fatal(err)
		}

		data, err := os.ReadFile(filepath.Join(mountpoint, "Channel/episode.mp4")) //nolint:gosec // G304: Controlled FUSE fixture below t.TempDir.
		if err != nil || string(data) != "controlled media" {
			t.Fatal(string(data), err)
		}

		if file, openErr := os.OpenFile(filepath.Join(mountpoint, "Channel/episode.mp4"), os.O_WRONLY, 0); openErr == nil { //nolint:gosec // G304: Controlled FUSE fixture below t.TempDir.
			closeTest(t, file)
			t.Fatal("helper exposed writable media")
		}

		if mount.Done() == nil || !mount.Ready() {
			t.Fatal("helper mount not ready")
		}

		if err = mount.Close(); err != nil {
			t.Fatal(err)
		}
	}

	mount, err := MountProviderWithHelper(mountpoint, provider, socket)
	if err != nil {
		t.Fatal(err)
	}

	stop()

	select {
	case <-mount.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("helper loss did not reach the service")
	}

	if err = mount.Close(); err == nil {
		t.Fatal("helper loss was not reported")
	}
}
