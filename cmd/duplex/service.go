package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"duplex/internal/admin"
	"duplex/internal/app"
	"duplex/internal/config"
	"duplex/internal/diagnostics"
	"duplex/internal/source"
)

func serve(ctx context.Context, args []string) (result error) {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	file := f.String("config", "config.yaml", "writable YAML settings file")
	noMount := f.Bool("no-mount", false, "administration test mode; readiness remains false")
	profileDirectory := f.String("profile-dir", "", "private absolute profiling directory; disabled by default")
	profileKind := f.String("profile-kind", "cpu", "one of cpu, allocs, block, or trace")
	profileDuration := f.Duration("profile-duration", time.Minute, "profile capture duration")
	yt := f.String("yt-dlp", "yt-dlp", "pinned yt-dlp executable")
	socket := f.String("mount-socket", "", "isolated FUSE mount helper socket")
	nfsAddress := f.String("nfs-listen", "", "read-only NFSv3 listener for the trusted Docker host")

	deno := f.String("deno", "deno", "pinned Deno executable")
	if e := f.Parse(args); e != nil {
		return e
	}

	manager, e := config.Open(*file)
	if e != nil {
		return e
	}

	c := manager.Snapshot().Config

	auth, e := admin.OpenAuth(filepath.Join(c.DataDir, "secrets", "admin.json"))
	if e != nil {
		return errors.New("cannot load administrator credentials; run duplex init first")
	}

	application, e := openService(ctx, manager, &source.YouTube{Executable: *yt, Deno: *deno}, *noMount, *socket, *nfsAddress)
	if e != nil {
		return e
	}

	defer func() { result = errors.Join(result, application.Close()) }()

	profile, e := startProfile(ctx, application, *profileDirectory, *profileKind, *profileDuration)
	if e != nil {
		return e
	}

	defer stopServiceProfile(profile)

	handler, e := admin.New(application, auth)
	if e != nil {
		return e
	}

	listener, e := (&net.ListenConfig{}).Listen(ctx, "tcp", c.Listen)
	if e != nil {
		return e
	}

	if !*noMount {
		if e = notifyReady(ctx, os.Getenv("NOTIFY_SOCKET")); e != nil {
			return errors.Join(e, listener.Close())
		}
	}

	fmt.Println("duplex service started")

	return serveMountedHTTP(ctx, listener, handler.Handler(), application.MountDone())
}

func stopServiceProfile(profile *diagnostics.Capture) {
	if err := profile.Stop(); err != nil {
		slog.Error("profile capture failed", "error", err)
	}
}

func openService(ctx context.Context, manager *config.Manager, adapter source.SourceAdapter, disabled bool, socket, address string) (*app.App, error) {
	if address != "" {
		if disabled || socket != "" {
			return nil, errors.New("nfs-listen cannot be combined with no-mount or mount-socket")
		}

		return app.OpenWithNFS(ctx, manager, adapter, address)
	}

	return app.OpenWithMountSocket(ctx, manager, adapter, disabled, socket)
}

func notifyReady(ctx context.Context, socket string) error {
	if socket == "" {
		return nil
	}

	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + socket[1:]
	}

	// The service manager supplies its local Unix datagram socket.
	connection, err := (&net.Dialer{}).DialContext(ctx, "unixgram", socket)
	if err != nil {
		return errors.New("service manager notification failed")
	}

	_, err = connection.Write([]byte("READY=1\nSTATUS=duplex mount and administration are ready"))

	return errors.Join(err, connection.Close())
}
