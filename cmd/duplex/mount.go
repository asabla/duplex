package main

import (
	"context"
	"errors"
	"net"
	"net/http"
)

func serveMountedHTTP(ctx context.Context, listener net.Listener, handler http.Handler, mountDone <-chan struct{}) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	lost := errors.New("library filesystem stopped; restart duplex")

	go func() {
		select {
		case <-ctx.Done():
		case <-mountDone:
			cancel(lost)
		}
	}()

	err := serveHTTP(ctx, listener, handler)
	if errors.Is(context.Cause(ctx), lost) {
		return errors.Join(err, lost)
	}

	return err
}
