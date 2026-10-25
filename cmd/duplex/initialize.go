package main

import (
	"flag"
	"fmt"
	"path/filepath"

	"duplex/internal/admin"
	"duplex/internal/config"
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
