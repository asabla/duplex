// duplex serves the household catalogue and the media qualification tools.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"duplex/internal/plex"
)

type local struct {
	f    *os.File
	size int64
	id   string
}

func (l *local) ID() string  { return l.id }
func (l *local) Size() int64 { return l.size }
func (l *local) ReadAt(ctx context.Context, p []byte, o int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return l.f.ReadAt(p, o)
}

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

func checkPlex(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("check-plex", flag.ContinueOnError)
	base := f.String("url", "http://127.0.0.1:32400", "Plex origin")
	tokenFile := f.String("token-file", "", "file containing Plex token (optional for isolated local test server)")
	if err := f.Parse(args); err != nil {
		return err
	}
	token := ""
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			return errors.New("cannot read Plex token file")
		}
		token = string(b)
	}
	c, err := plex.New(*base, token)
	if err != nil {
		return err
	}
	result, err := c.Check(ctx)
	if er := json.NewEncoder(os.Stdout).Encode(result); er != nil {
		return er
	}
	return err
}
