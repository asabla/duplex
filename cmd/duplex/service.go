package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"duplex/internal/admin"
	"duplex/internal/app"
	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/source"
)

func initialize(args []string) error {
	f := flag.NewFlagSet("init", flag.ContinueOnError)
	file := f.String("config", "config.yaml", "writable YAML settings file")
	base := f.String("state-dir", "var", "dedicated state directory")
	listen := f.String("listen", "127.0.0.1:8765", "admin listener")
	mountpoint := f.String("mountpoint", "", "optional Linux mount directory")
	cacheDir := f.String("cache-dir", "", "optional media cache directory")
	plexURL := f.String("plex-url", "", "Plex origin")
	plexSection := f.String("plex-section", "", "dedicated NFO series library section")
	plexToken := f.String("plex-token-file", "", "external token file path")
	if e := f.Parse(args); e != nil {
		return e
	}
	root, e := filepath.Abs(*base)
	if e != nil {
		return e
	}
	if e = config.Create(*file, root); e != nil {
		return e
	}
	manager, e := config.Open(*file)
	if e != nil {
		return e
	}
	snap := manager.Snapshot()
	snap.Config.Listen = *listen
	if *mountpoint != "" {
		snap.Config.Mountpoint = *mountpoint
	}
	if *cacheDir != "" {
		snap.Config.CacheDir = *cacheDir
	}
	snap.Config.Plex = config.Plex{URL: *plexURL, Section: *plexSection, TokenFile: *plexToken}
	if e = manager.Save(snap.Config, snap.Revision); e != nil {
		return e
	}
	password, e := admin.InitCredentials(filepath.Join(root, "data", "secrets"))
	if e != nil {
		return e
	}
	fmt.Printf("Created configuration. Administrator: admin\nRead the initial password from %s\n", password)
	return nil
}

func serve(ctx context.Context, args []string) (result error) {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	file := f.String("config", "config.yaml", "writable YAML settings file")
	noMount := f.Bool("no-mount", false, "administration test mode; readiness remains false")
	profileDirectory := f.String("profile-dir", "", "private absolute profiling directory; disabled by default")
	profileKind := f.String("profile-kind", "cpu", "one of cpu, allocs, block, or trace")
	profileDuration := f.Duration("profile-duration", time.Minute, "profile capture duration")
	yt := f.String("yt-dlp", "yt-dlp", "pinned yt-dlp executable")
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
	application, e := app.Open(ctx, manager, &source.YouTube{Executable: *yt, Deno: *deno}, *noMount)
	if e != nil {
		return e
	}
	defer func() { result = errors.Join(result, application.Close()) }()
	profile, e := startProfile(ctx, application, *profileDirectory, *profileKind, *profileDuration)
	if e != nil {
		return e
	}
	defer func() {
		if err := profile.Stop(); err != nil {
			slog.Error("profile capture failed", "error", err)
		}
	}()
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
	return serveHTTP(ctx, listener, handler.Handler())
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

func health(ctx context.Context, args []string) (result error) {
	f := flag.NewFlagSet("health", flag.ContinueOnError)
	url := f.String("url", "http://127.0.0.1:8765/mountz", "health URL")
	if e := f.Parse(args); e != nil {
		return e
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("health redirects rejected") }}
	request, e := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if e != nil {
		return e
	}
	response, e := client.Do(request)
	if e != nil {
		return errors.New("service is unavailable")
	}
	defer func() { result = errors.Join(result, response.Body.Close()) }()
	if response.StatusCode != 200 {
		return errors.New("service is not ready")
	}
	return nil
}

func backup(ctx context.Context, args []string) (result error) {
	f := flag.NewFlagSet("backup", flag.ContinueOnError)
	file := f.String("config", "config.yaml", "YAML settings file")
	output := f.String("output", "", "new SQLite backup file")
	if e := f.Parse(args); e != nil {
		return e
	}
	if *output == "" {
		return errors.New("--output is required")
	}
	m, e := config.Open(*file)
	if e != nil {
		return e
	}
	s, e := catalog.OpenForBackupContext(ctx, filepath.Join(m.Snapshot().Config.DataDir, "catalogue.sqlite"))
	if e != nil {
		return e
	}
	defer func() { result = errors.Join(result, s.Close()) }()
	return s.Backup(ctx, *output)
}
