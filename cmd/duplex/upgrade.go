package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"

	"duplex/internal/app"
	"duplex/internal/config"
)

func upgradeLayouts(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("upgrade-layouts", flag.ContinueOnError)
	file := flags.String("config", "config.yaml", "YAML settings file; stop Duplex before maintenance")
	video := flags.String("video", "", "one recording ID; omitted means all retained recordings")

	apply := flags.Bool("apply", false, "apply changes; omitted means preview only")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() != 0 {
		return errors.New("upgrade-layouts accepts only named options")
	}

	manager, err := config.Open(*file)
	if err != nil {
		return err
	}

	report, err := app.UpgradeLayouts(ctx, manager.Snapshot().Config, *video, *apply)

	return errors.Join(err, json.NewEncoder(os.Stdout).Encode(report))
}
