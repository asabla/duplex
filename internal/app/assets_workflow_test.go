package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"duplex/internal/config"
	"duplex/internal/library"
	"duplex/internal/source"
)

type (
	assetFixtureResponse struct {
		data    []byte
		status  int
		failure error
	}
	assetFixtureTransport map[string]assetFixtureResponse
)

func (f assetFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, ok := f[r.URL.Path]
	if !ok {
		return nil, errors.New("unexpected asset request")
	}

	if response.failure != nil {
		return nil, response.failure
	}

	status := response.status
	if status == 0 {
		status = http.StatusOK
	}

	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(response.data)), Request: r}, nil
}

func TestOptionalAssetsRetainValidImagesAndOriginalCaptions(t *testing.T) {
	a := pipelineApp(t, pipelineChannel(config.DefaultSelection()), fixtureCollection(1))
	setCaptionFetching(t, a, true)

	picture := image.NewRGBA(image.Rect(0, 0, 128, 72))
	picture.Set(20, 20, color.White)

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}

	transport := assetFixtureTransport{
		"/poster":       {data: encoded.Bytes()},
		"/captions.srt": {status: http.StatusServiceUnavailable},
		"/captions.vtt": {data: []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nOriginal &amp; clear\n\n")},
		"/broken":       {data: []byte("invalid image")},
	}
	previous := http.DefaultTransport
	http.DefaultTransport = transport

	t.Cleanup(func() { http.DefaultTransport = previous })

	metadata := source.Metadata{ID: "video000000", ChannelID: testOwner, Thumbnail: "https://i.ytimg.com/poster", Language: "fr", Subtitles: map[string][]source.Subtitle{"en-GB": {{Ext: "srt", URL: "https://www.youtube.com/captions.srt"}, {Ext: "vtt", URL: "https://www.youtube.com/captions.vtt"}}}}
	candidate := source.Candidate{Audio: &source.Format{Language: "en"}}

	entries, err := a.acquireAssets(t.Context(), metadata, candidate)
	if err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
	}

	assets := map[string]library.Entry{}
	for _, entry := range entries {
		assets[entry.Path] = entry
	}

	poster := assets[".jpg"].Media

	data := make([]byte, poster.Size())
	if _, err = poster.ReadAt(t.Context(), data, 0); err != nil {
		t.Fatal(err)
	}

	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil || decoded.Bounds().Dx() != 128 || decoded.Bounds().Dy() != 72 {
		t.Fatal("thumbnail proportions changed", err)
	}

	subtitle := assets[".en.srt"].Media

	data = make([]byte, subtitle.Size())
	if _, err = subtitle.ReadAt(t.Context(), data, 0); err != nil || !strings.Contains(string(data), "00:00:00,000 --> 00:00:01,000\nOriginal & clear") {
		t.Fatal(string(data), err)
	}

	metadata.Thumbnail = "https://i.ytimg.com/broken"
	metadata.Subtitles = nil
	metadata.Language = ""

	retained, err := a.acquireAssets(t.Context(), metadata, source.Candidate{})
	if err == nil || len(retained) != 2 {
		t.Fatal("failed optional refresh must retain last valid assets", retained, err)
	}

	for _, entry := range a.loadAssets(metadata.ID) {
		if entry.Media.ID() != assets[entry.Path].Media.ID() {
			t.Fatal("optional failure replaced valid content")
		}
	}

	a.original.DataDir = filepath.Dir(a.original.DataDir) + "/new-assets"
	metadata.Thumbnail = ""

	fallback, err := a.acquireAssets(t.Context(), metadata, source.Candidate{})
	if err == nil || len(fallback) != 1 || fallback[0].Path != ".jpg" {
		t.Fatal("missing image must have a deterministic fallback", fallback, err)
	}

	if got := a.loadAssets(metadata.ID); len(got) != 1 || got[0].Media.ID() != fallback[0].Media.ID() {
		t.Fatal("fallback was not durable", got)
	}
}

func setCaptionFetching(t testing.TB, a *App, enabled bool) {
	t.Helper()

	snapshot := a.Config.Snapshot()
	snapshot.Config.FetchCaptions = enabled

	if err := a.Config.Save(snapshot.Config, snapshot.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestCaptionFetchingDefaultsOffAndCanChangeWithoutRestart(t *testing.T) {
	a := pipelineApp(t, pipelineChannel(config.DefaultSelection()), fixtureCollection(1))
	metadata := source.Metadata{ID: "video000000", ChannelID: testOwner, Thumbnail: "https://i.ytimg.com/poster", Language: "en", Subtitles: map[string][]source.Subtitle{"en": {{Ext: "srt", URL: "https://www.youtube.com/captions"}}}}

	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 128, 72))); err != nil {
		t.Fatal(err)
	}

	transport := &scanAssetTransport{base: assetFixtureTransport{
		"/poster":   {data: picture.Bytes()},
		"/captions": {status: http.StatusTooManyRequests},
	}}
	previous := http.DefaultTransport
	http.DefaultTransport = transport

	t.Cleanup(func() { http.DefaultTransport = previous })

	entries, err := a.acquireAssets(t.Context(), metadata, source.Candidate{})
	if err != nil || len(entries) != 1 || entries[0].Path != ".jpg" || transport.requests.Load() != 1 {
		t.Fatal("default asset work fetched captions or lost artwork", entries, err, transport.requests.Load())
	}

	setCaptionFetching(t, a, true)

	if _, err = a.acquireAssets(t.Context(), metadata, source.Candidate{}); err == nil || transport.requests.Load() != 3 {
		t.Fatal("enabled caption failure was not reported", err, transport.requests.Load())
	}

	transport.base["/captions"] = assetFixtureResponse{data: []byte("original captions")}

	entries, err = a.acquireAssets(t.Context(), metadata, source.Candidate{})
	if err != nil || len(entries) != 2 || transport.requests.Load() != 5 {
		t.Fatal("enabled captions were not fetched", entries, err, transport.requests.Load())
	}

	setCaptionFetching(t, a, false)

	transport.base["/captions"] = assetFixtureResponse{status: http.StatusTooManyRequests}

	retained, err := a.acquireAssets(t.Context(), metadata, source.Candidate{})
	if err != nil || len(retained) != 2 || transport.requests.Load() != 6 {
		t.Fatal("disabled caption work retried or lost stored captions", retained, err, transport.requests.Load())
	}

	for i, entry := range retained {
		if entry.Media.ID() != entries[i].Media.ID() {
			t.Fatal("disabling captions changed stored assets")
		}
	}
}

func TestAssetFailuresAreBoundedAndExplicit(t *testing.T) {
	previous := http.DefaultTransport
	http.DefaultTransport = assetFixtureTransport{
		"/large":   {data: bytes.Repeat([]byte("x"), 8<<20+1)},
		"/missing": {status: http.StatusNotFound},
		"/offline": {failure: errors.New("private signed locator must not escape")},
		"/empty":   {},
	}

	t.Cleanup(func() { http.DefaultTransport = previous })

	for _, raw := range []string{"http://localhost/private", "https://i.ytimg.com/large", "https://i.ytimg.com/missing", "https://i.ytimg.com/offline"} {
		if _, err := fetchAsset(t.Context(), raw); err == nil || strings.Contains(err.Error(), "private signed") {
			t.Fatal("asset failure missing or unsafe", err)
		}
	}

	a := pipelineApp(t, pipelineChannel(config.DefaultSelection()), fixtureCollection(1))
	if _, err := a.captionAsset(t.Context(), source.Metadata{}, "../en"); err == nil {
		t.Fatal("unsafe subtitle language accepted")
	}

	if _, err := a.fetchCaption(t.Context(), "video000000", "en", source.Subtitle{Ext: "vtt", URL: "https://www.youtube.com/empty"}); err == nil {
		t.Fatal("empty captions accepted")
	}

	if _, err := a.saveAsset("../outside", ".jpg", []byte("x")); err == nil {
		t.Fatal("unsafe asset identity accepted")
	}

	if err := a.saveAssetManifest("video000000", []library.Entry{{Path: ".jpg", Media: mountedBytes{}}}); err == nil {
		t.Fatal("non-durable manifest entry accepted")
	}

	if err := a.saveAssetManifest("../outside", nil); err == nil {
		t.Fatal("unsafe manifest identity accepted")
	}

	entry, err := a.saveAsset("video000000", ".jpg", []byte("unchanged"))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err = entry.Media.ReadAt(ctx, make([]byte, 1), 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if err = a.saveAssetManifest("video000000", []library.Entry{entry}); err != nil {
		t.Fatal(err)
	}

	asset, ok := entry.Media.(localAsset)
	if !ok {
		t.Fatal("saved asset is not local")
	}

	if err = os.Remove(asset.file); err != nil {
		t.Fatal(err)
	}

	if len(a.loadAssets("video000000")) != 0 {
		t.Fatal("missing asset accepted")
	}

	if _, err = asset.ReadAt(t.Context(), make([]byte, 1), 0); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestOriginalSubtitleLanguageAndTrackFallbacks(t *testing.T) {
	for _, tc := range []struct {
		metadata  source.Metadata
		candidate source.Candidate
		want      string
	}{
		{source.Metadata{Language: "fr"}, source.Candidate{Audio: &source.Format{Language: "en"}}, "en"},
		{source.Metadata{Language: "fr"}, source.Candidate{Video: source.Format{Language: "de"}}, "fr"},
		{source.Metadata{}, source.Candidate{Video: source.Format{Language: "de"}}, "de"},
		{source.Metadata{AutomaticCaptions: map[string][]source.Subtitle{"fr-orig": {}, "en-orig": {}}}, source.Candidate{}, "en"},
		{source.Metadata{}, source.Candidate{}, ""},
	} {
		if got := subtitleLanguage(tc.metadata, tc.candidate); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}

	for _, key := range []string{"en", "en-orig"} {
		metadata := source.Metadata{AutomaticCaptions: map[string][]source.Subtitle{key: {{Ext: "vtt", URL: "original"}}}}
		if got := subtitleTracks(metadata, "en"); len(got) != 1 || got[0].URL != "original" {
			t.Fatal(key, got)
		}
	}
}

func TestAutomaticCaptionsMatchOriginalAudioRegion(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		language string
		tracks   map[string][]source.Subtitle
		want     string
	}{
		{"base original", "en-US", map[string][]source.Subtitle{"en-orig": {{URL: "original"}}, "ar-orig": {{URL: "other audio"}}}, "original"},
		{"prefer original", "en-US", map[string][]source.Subtitle{"en-orig": {{URL: "original"}}, "en-US": {{URL: "regional translation"}}}, "original"},
		{"exact original", "en-US", map[string][]source.Subtitle{"en-US-orig": {{URL: "exact"}}, "en-orig": {{URL: "base"}}}, "exact"},
		{"base fallback", "en-US", map[string][]source.Subtitle{"en": {{URL: "base"}}}, "base"},
		{"unrelated language", "en-US", map[string][]source.Subtitle{"ar-orig": {{URL: "Arabic"}}}, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			got := subtitleTracks(source.Metadata{AutomaticCaptions: scenario.tracks}, scenario.language)
			if scenario.want == "" {
				if len(got) != 0 {
					t.Fatal("unrelated captions selected", got)
				}

				return
			}

			if len(got) != 1 || got[0].URL != scenario.want {
				t.Fatal("original audio caption language was not matched", got, scenario.want)
			}
		})
	}
}
