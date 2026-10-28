//go:build linux

package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"duplex/internal/nfs"
)

// Each profile uses a separate port and private handle database, so Linux cannot
// reuse another profile's superblock and silently share its attribute settings.
func benchmarkNFSMount(b *testing.B, provider catalogueFS, attributeSeconds int) string {
	b.Helper()

	ctx, cancel := context.WithCancel(context.WithoutCancel(b.Context()))
	b.Cleanup(cancel)

	server, err := nfs.Listen(ctx, "127.0.0.1:0", b.TempDir(), provider)
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		if err := server.Close(); err != nil {
			b.Error(err)
		}
	})

	_, port, err := net.SplitHostPort(server.Addr().String())
	if err != nil {
		b.Fatal(err)
	}

	mounted := filepath.Join(b.TempDir(), "mount")
	if err = os.Mkdir(mounted, 0o700); err != nil {
		b.Fatal(err)
	}

	options := fmt.Sprintf("addr=127.0.0.1,port=%s,mountport=%s,nfsvers=3,tcp,mountproto=tcp,nolock,noacl,ro,hard,timeo=10,retrans=2,actimeo=%d,lookupcache=positive", port, port, attributeSeconds)

	command := exec.CommandContext(b.Context(), "mount", "-t", "nfs", "-o", options, ":/media", mounted) //nolint:gosec // Fixed Linux mount tool; the listener and destination are private benchmark resources.
	if output, err := command.CombinedOutput(); err != nil {
		b.Fatalf("NFS benchmark mount: %v: %s", err, output)
	}

	b.Cleanup(func() {
		command := exec.CommandContext(ctx, "umount", mounted) //nolint:gosec // Fixed Linux unmount tool for the private benchmark mount; its server lives through cleanup.
		if output, err := command.CombinedOutput(); err != nil {
			b.Errorf("NFS benchmark unmount: %v: %s", err, output)
		}
	})

	return mounted
}
