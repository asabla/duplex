package app

import (
	"io"
	"net/http"

	"duplex/internal/diagnostics"
)

type measuredTransport struct {
	base     http.RoundTripper
	recorder *diagnostics.Recorder
}

func (t measuredTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.recorder.Count("media.http_requests", 1)
	response, err := t.base.RoundTrip(request)
	if response != nil && response.Body != nil {
		response.Body = measuredBody{ReadCloser: response.Body, recorder: t.recorder}
	}
	return response, err
}

type measuredBody struct {
	io.ReadCloser
	recorder *diagnostics.Recorder
}

func (b measuredBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	b.recorder.Count("media.upstream_bytes", int64(n))
	return n, err
}
