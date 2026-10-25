package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"

	"duplex/internal/plex"
)

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
