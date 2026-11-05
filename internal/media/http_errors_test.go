package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
)

type bodyFailureTransport struct {
	fail   func() error
	closed *bool
}

func (transport bodyFailureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	rangeHeader := "bytes 0-3/4"
	if request.Header.Get("Range") == "bytes=0-0" {
		rangeHeader = "bytes 0-0/4"
	}

	return &http.Response{StatusCode: http.StatusPartialContent, Header: http.Header{"Content-Range": []string{rangeHeader}}, Body: failedBody(transport), Request: request}, nil
}

type failedBody struct {
	fail   func() error
	closed *bool
}

func (body failedBody) Read(p []byte) (int, error) {
	return copy(p, "xx"), body.fail()
}

func (body failedBody) Close() error {
	*body.closed = true
	return nil
}

func TestHTTPBodyErrorsPreserveCancellationAndTimeouts(t *testing.T) {
	for _, probe := range []bool{false, true} {
		for _, test := range []struct {
			name   string
			cause  error
			want   error
			cancel bool
		}{
			{"cancel during body read", context.Canceled, context.Canceled, true},
			{"wrapped cancellation", fmt.Errorf("private-locator: %w", context.Canceled), context.Canceled, false},
			{"wrapped deadline", fmt.Errorf("private-locator: %w", context.DeadlineExceeded), context.DeadlineExceeded, false},
			{"client body timeout", &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}, context.DeadlineExceeded, false},
			{"other failure", errors.New("private-locator"), nil, false},
		} {
			t.Run(fmt.Sprintf("probe=%v/%s", probe, test.name), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				closed := false
				client := &http.Client{Transport: bodyFailureTransport{closed: &closed, fail: func() error {
					if test.cancel {
						cancel()
					}

					return test.cause
				}}}

				source := &HTTPSource{client: client, url: "https://fixture.test/media", size: 4, retries: &RetryPolicy{}, retryIdentity: "fixture"}

				var err error
				if probe {
					err = source.probe(ctx)
				} else {
					_, err = source.ReadAt(ctx, make([]byte, 4), 0)
				}

				if err == nil || !closed || (test.want != nil && !errors.Is(err, test.want)) || strings.Contains(err.Error(), "private-locator") || errors.Is(err, io.EOF) {
					t.Fatal("body failure lost its class, cleanup, or privacy", err, closed)
				}
			})
		}
	}
}
