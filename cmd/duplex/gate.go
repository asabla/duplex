package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"duplex/internal/cache"
	"duplex/internal/library"
	"duplex/internal/media"
)

type gateOptions struct {
	video, audio, sourceJSON, videoFormat, audioFormat, combinedFormat string
	listen, mountpoint, cacheDir, fixtureCatalogue                     string
	cacheBytes                                                         int64
	fixtureEpisodes                                                    int
}

func parseGate(args []string) (gateOptions, error) {
	var o gateOptions
	f := flag.NewFlagSet("gate", flag.ContinueOnError)
	f.StringVar(&o.video, "video", "", "local controlled video-only MP4 fixture")
	f.StringVar(&o.audio, "audio", "", "local controlled audio-only MP4 fixture")
	f.StringVar(&o.sourceJSON, "source-json", "", "fresh private yt-dlp JSON; signed locators are never logged")
	f.StringVar(&o.videoFormat, "video-format", "401", "YouTube video format for qualification")
	f.StringVar(&o.audioFormat, "audio-format", "140", "YouTube audio format for qualification")
	f.StringVar(&o.combinedFormat, "combined-format", "", "explicit finite combined MP4 format instead of separate tracks")
	f.StringVar(&o.listen, "listen", "127.0.0.1:8765", "qualification HTTP address")
	f.StringVar(&o.mountpoint, "mount", "", "Linux FUSE mount directory")
	f.StringVar(&o.cacheDir, "cache-dir", "var/media", "source chunk cache directory")
	f.Int64Var(&o.cacheBytes, "cache-bytes", 50*1024*1024*1024, "media cache limit")
	f.IntVar(&o.fixtureEpisodes, "fixture-episodes", 1, "qualification episode count")
	f.StringVar(&o.fixtureCatalogue, "fixture-catalogue", "", "optional controlled episode metadata JSON; reloads atomically")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	return o, nil
}

type gateInput struct {
	video, audio                            media.Representation
	remote                                  []*media.HTTPSource
	local                                   []*os.File
	store                                   *cache.Store
	videoID, channelID, title, channelTitle string
	year                                    int
}

func (in *gateInput) close() error {
	var failures []error
	for _, f := range in.local {
		failures = append(failures, f.Close())
	}
	if in.store != nil {
		failures = append(failures, in.store.Close())
	}
	return errors.Join(failures...)
}

func (in *gateInput) load(ctx context.Context, o gateOptions) error {
	if o.sourceJSON != "" {
		loaded, err := loadRemoteGate(ctx, o)
		*in = loaded
		return err
	}

	if o.combinedFormat != "" {
		return errors.New("--combined-format requires --source-json")
	}
	video, err := openLocal(o.video)
	if err != nil {
		return err
	}
	in.local = append(in.local, video.f)
	audio, err := openLocal(o.audio)
	if err != nil {
		return err
	}
	in.local = append(in.local, audio.f)
	in.video, in.audio = video, audio
	return nil
}

func loadRemoteGate(ctx context.Context, o gateOptions) (in gateInput, err error) {
	in = defaultGateInput()
	var info struct {
		ID         string `json:"id"`
		ChannelID  string `json:"channel_id"`
		Title      string `json:"title"`
		LiveStatus string `json:"live_status"`
		Channel    string `json:"channel"`
		UploadDate string `json:"upload_date"`
		Formats    []struct {
			FormatID string `json:"format_id"`
			Locator  string `json:"url"`
		} `json:"formats"`
	}
	b, err := os.ReadFile(o.sourceJSON)
	if err != nil {
		return in, errors.New("cannot read source metadata")
	}
	if err = json.Unmarshal(b, &info); err != nil {
		return in, errors.New("invalid source metadata")
	}
	if info.LiveStatus != "not_live" && info.LiveStatus != "was_live" {
		return in, errors.New("qualification requires a finite recorded upload or replay")
	}
	urls := map[string]string{}
	for _, format := range info.Formats {
		urls[format.FormatID] = format.Locator
	}
	store, err := cache.New(o.cacheDir, o.cacheBytes)
	if err != nil {
		return in, err
	}
	in.store = store
	client := &http.Client{Timeout: 30 * time.Second}
	formats := []string{o.videoFormat, o.audioFormat}
	if o.combinedFormat != "" {
		formats = []string{o.combinedFormat}
	}
	for _, id := range formats {
		if urls[id] == "" {
			return in, errors.New("selected format is absent")
		}
		source, er := media.OpenHTTP(ctx, client, urls[id], info.ID+":"+id, store)
		if er != nil {
			return in, er
		}
		in.remote = append(in.remote, source)
	}
	in.video = in.remote[0]
	if len(in.remote) == 2 {
		in.audio = in.remote[1]
	}
	in.videoID, in.channelID, in.title = info.ID, info.ChannelID, info.Title
	if info.Channel != "" {
		in.channelTitle = info.Channel
	}
	if published, e := time.Parse("20060102", info.UploadDate); e == nil {
		in.year = published.Year()
	}
	return in, nil
}

func defaultGateInput() gateInput {
	return gateInput{videoID: "fixture-video", channelID: "fixture-channel", title: "Controlled media fixture", year: 2026, channelTitle: "duplex qualification"}
}

func openLocal(path string) (*local, error) {
	if path == "" {
		return nil, errors.New("both local tracks or --source-json are required")
	}
	// Operator-supplied qualification fixture; no HTTP input reaches this path.
	file, err := os.Open(path) //nolint:gosec // G304: CLI qualification reads the operator's selected local fixture.
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(errors.New("fixture must be a regular file"), file.Close())
	}
	return &local{f: file, size: info.Size(), id: filepath.Base(path)}, nil
}

func gateEntries(o gateOptions, in gateInput, virtual media.Representation) ([]library.Entry, error) {
	episodes := []library.Episode{}
	if o.fixtureCatalogue != "" {
		var items []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Year   int    `json:"year"`
			Number int    `json:"number"`
		}
		b, e := os.ReadFile(o.fixtureCatalogue)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &items); e != nil {
			return nil, e
		}
		for _, item := range items {
			episodes = append(episodes, library.Episode{ChannelID: in.channelID, ChannelTitle: in.channelTitle, VideoID: item.ID, Title: item.Title, Season: item.Year, Number: item.Number, Media: virtual})
		}
	} else {
		for i := 1; i <= o.fixtureEpisodes; i++ {
			id, name := in.videoID, in.title
			if o.fixtureEpisodes > 1 {
				id = fmt.Sprintf("%s-%d", in.videoID, i)
				name = fmt.Sprintf("%s %d", in.title, i)
			}
			episodes = append(episodes, library.Episode{ChannelID: in.channelID, ChannelTitle: in.channelTitle, VideoID: id, Title: name, Season: in.year, Number: i, Media: virtual})
		}
	}
	return library.Entries(episodes)
}

func gateCommand(ctx context.Context, args []string) (result error) {
	o, err := parseGate(args)
	if err != nil {
		return err
	}
	if o.fixtureEpisodes < 1 || o.fixtureEpisodes > 20 {
		return errors.New("fixture episode count must be between 1 and 20")
	}
	in := defaultGateInput()
	defer func() { result = errors.Join(result, in.close()) }()
	if err = in.load(ctx, o); err != nil {
		return err
	}
	virtual, spans, profile, err := gateRepresentation(ctx, in)
	if err != nil {
		return err
	}
	if o.mountpoint != "" {
		mount, err := mountGate(ctx, o, in, virtual)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, mount.Close()) }()
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", o.listen)
	if err != nil {
		return err
	}
	slog.Info("qualification slice ready", "address", listener.Addr().String(), "output_bytes", virtual.Size(), "packaging_version", profile)
	return serveHTTP(ctx, listener, gateHandler(o, in, virtual, spans, profile))
}

func gateRepresentation(ctx context.Context, in gateInput) (media.Representation, int, string, error) {
	if in.audio == nil {
		return in.video, 0, "finite-combined-mp4", nil
	}
	muxed, err := media.Mux(ctx, in.video, in.audio)
	if err != nil {
		return nil, 0, "", err
	}
	return muxed, len(muxed.Spans), media.PackagingVersion, nil
}

func gateHandler(o gateOptions, in gateInput, virtual media.Representation, spanCount int, profile string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /media.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", fmt.Sprintf("%q", virtual.ID()))
		http.ServeContent(w, r, "media.mp4", time.Time{}, &media.Seeker{Context: r.Context(), Source: virtual})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeText(w, "ok\n") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { writeText(w, "qualification slice ready\n") })
	mux.HandleFunc("GET /diagnostics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var upstream int64
		for _, s := range in.remote {
			upstream += s.UpstreamBytes.Load()
		}
		status := map[string]any{"mode": "qualification", "packaging_version": profile, "output_bytes": virtual.Size(), "spans": spanCount, "upstream_bytes": upstream, "combined_format": o.combinedFormat}
		if in.store != nil {
			status["cache"] = in.store.Stats()
		}
		writeJSON(w, status)
	})
	return mux
}

func mountGate(ctx context.Context, o gateOptions, in gateInput, virtual media.Representation) (*library.Mount, error) {
	entries, err := gateEntries(o, in, virtual)
	if err != nil {
		return nil, err
	}
	mount, err := library.MountReadOnly(o.mountpoint, entries)
	if err != nil {
		return nil, err
	}
	if o.fixtureCatalogue != "" {
		go reloadGate(ctx, o, in, virtual, mount)
	}
	return mount, nil
}

func reloadGate(ctx context.Context, o gateOptions, in gateInput, virtual media.Representation, mount *library.Mount) {
	var last [32]byte
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		data, err := os.ReadFile(o.fixtureCatalogue)
		if err != nil {
			slog.Error("fixture catalogue read failed")
			continue
		}
		checksum := sha256.Sum256(data)
		if checksum == last {
			continue
		}
		entries, err := gateEntries(o, in, virtual)
		if err == nil {
			err = mount.Update(entries)
		}
		if err != nil {
			slog.Error("fixture catalogue publication failed")
			continue
		}
		last = checksum
	}
}
