// duplex serves the household catalogue and the media qualification tools.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}

		slog.Error("duplex stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return execute(ctx, os.Args[1:])
}

func execute(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: duplex init|serve|backup|check-plex|health|gate; use COMMAND --help for options")
	}

	switch args[0] {
	case "init":
		return initialize(args[1:])
	case "serve":
		return serve(ctx, args[1:])
	case "backup":
		return backup(ctx, args[1:])
	case "health":
		return health(ctx, args[1:])
	case "fixture-http":
		return fixtureHTTP(ctx, args[1:])
	case "check-plex":
		return checkPlex(ctx, args[1:])
	case "gate":
		return gateCommand(ctx, args[1:])
	default:
		return errors.New("unknown command; use duplex init|serve|backup|check-plex|health|gate")
	}
}
