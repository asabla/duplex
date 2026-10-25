package app

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"duplex/internal/source"
)

type rejectingClientTransport struct{ base http.RoundTripper }

func (transport rejectingClientTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Query().Get("c") == "TVHTML5" {
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: request}, nil
	}
	return transport.base.RoundTrip(request)
}

func TestPreparationUsesAlternativeClientWithoutChangingLanguage(t *testing.T) {
	app, adapter, _ := mediaPipeline(t)
	metadata := adapter.metadata["video000000"]
	original := metadata.Formats
	metadata.Formats = nil
	for _, client := range []string{"TVHTML5", "VISIONOS"} {
		for _, format := range original {
			format.ID += "-" + client
			format.URL += "&c=" + client
			format.LanguagePreference = 10
			metadata.Formats = append(metadata.Formats, format)
		}
	}
	app.httpClient.Transport = rejectingClientTransport{base: app.httpClient.Transport}
	prepared, err := app.Prepare(t.Context(), metadata, 1080)
	if err != nil || prepared.Height != 1080 || prepared.Fallback == "" {
		t.Fatal("alternative client did not prepare", prepared.Height, err)
	}
	data, err := os.ReadFile(filepath.Join(app.original.IndexDir, prepared.Revision+".description.json"))
	if err != nil {
		t.Fatal(err)
	}
	var descriptor struct{ VideoFormat, AudioFormat string }
	if err = json.Unmarshal(data, &descriptor); err != nil {
		t.Fatal(err)
	}
	if descriptor.VideoFormat != "137-VISIONOS" || descriptor.AudioFormat != "140-VISIONOS" {
		t.Fatal("selected incompatible client or audio", descriptor)
	}
	if got := source.Candidates(metadata, 1080); len(got) != 2 {
		t.Fatal("unexpected candidate count", len(got))
	}
}
