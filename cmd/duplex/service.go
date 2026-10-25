package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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
func serve(args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	file := f.String("config", "config.yaml", "writable YAML settings file")
	noMount := f.Bool("no-mount", false, "administration test mode; readiness remains false")
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application, e := app.Open(ctx, manager, &source.YouTube{Executable: *yt, Deno: *deno}, *noMount)
	if e != nil {
		return e
	}
	defer application.Close()
	handler, e := admin.New(application, auth)
	if e != nil {
		return e
	}
	listener, e := net.Listen("tcp", c.Listen)
	if e != nil {
		return e
	}
	server := &http.Server{Handler: handler.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	if socket := os.Getenv("NOTIFY_SOCKET"); socket != "" {
		if strings.HasPrefix(socket, "@") {
			socket = "\x00" + socket[1:]
		}
		if connection, e := net.Dial("unixgram", socket); e == nil {
			_, _ = connection.Write([]byte("READY=1\nSTATUS=duplex mount and administration are ready"))
			connection.Close()
		}
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	fmt.Println("duplex service started")
	e = server.Serve(listener)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func health(args []string) error {
	f := flag.NewFlagSet("health", flag.ContinueOnError)
	url := f.String("url", "http://127.0.0.1:8765/mountz", "health URL")
	if e := f.Parse(args); e != nil {
		return e
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("health redirects rejected") }}
	response, e := client.Get(*url)
	if e != nil {
		return errors.New("service is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("service is not ready")
	}
	return nil
}
func backup(args []string) error {
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
	s, e := catalog.OpenForBackup(filepath.Join(m.Snapshot().Config.DataDir, "catalogue.sqlite"))
	if e != nil {
		return e
	}
	defer s.Close()
	return s.Backup(context.Background(), *output)
}
