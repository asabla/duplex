package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"time"
)

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
