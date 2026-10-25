package main

import (
	"context"
	"errors"
	"flag"
	"path/filepath"

	"duplex/internal/catalog"
	"duplex/internal/config"
)

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
