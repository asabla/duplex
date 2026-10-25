// duplex serves the household catalogue and the media qualification tools.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"duplex/internal/cache"
	"duplex/internal/library"
	"duplex/internal/media"
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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			return initialize(os.Args[2:])
		case "serve":
			return serve(os.Args[2:])
		case "backup":
			return backup(os.Args[2:])
		case "health":
			return health(os.Args[2:])
		case "fixture-http":
			return fixtureHTTP(os.Args[2:])
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "check-plex" {
		return checkPlex(os.Args[2:])
	}
	if len(os.Args) < 2 || os.Args[1] != "gate" {
		return errors.New("usage: duplex init|serve|backup|check-plex|health|gate; use COMMAND --help for options")
	}
	f := flag.NewFlagSet("gate", flag.ContinueOnError)
	video := f.String("video", "", "local controlled video-only MP4 fixture")
	audio := f.String("audio", "", "local controlled audio-only MP4 fixture")
	sourceJSON := f.String("source-json", "", "fresh private yt-dlp JSON; signed locators are never logged")
	videoFormat := f.String("video-format", "401", "YouTube video format for qualification")
	audioFormat := f.String("audio-format", "140", "YouTube audio format for qualification")
	combinedFormat := f.String("combined-format", "", "explicit finite combined MP4 format instead of separate tracks")
	listen := f.String("listen", "127.0.0.1:8765", "qualification HTTP address")
	mountpoint := f.String("mount", "", "Linux FUSE mount directory")
	cacheDir := f.String("cache-dir", "var/media", "source chunk cache directory")
	cacheBytes := f.Int64("cache-bytes", 50*1024*1024*1024, "media cache limit")
	fixtureEpisodes := f.Int("fixture-episodes", 1, "qualification episode count")
	fixtureCatalogue := f.String("fixture-catalogue", "", "optional controlled episode metadata JSON; reloads atomically")
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var v, a media.Representation
	videoID, channelID, title := "fixture-video", "fixture-channel", "Controlled media fixture"
	year, channelTitle := 2026, "duplex qualification"
	var remote []*media.HTTPSource
	var store *cache.Store
	if *sourceJSON != "" {
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
		b, err := os.ReadFile(*sourceJSON)
		if err != nil {
			return errors.New("cannot read source metadata")
		}
		if err = json.Unmarshal(b, &info); err != nil {
			return errors.New("invalid source metadata")
		}
		if info.LiveStatus != "not_live" && info.LiveStatus != "was_live" {
			return errors.New("qualification requires a finite recorded upload or replay")
		}
		urls := map[string]string{}
		for _, format := range info.Formats {
			urls[format.FormatID] = format.Locator
		}
		store, err = cache.New(*cacheDir, *cacheBytes)
		if err != nil {
			return err
		}
		defer store.Close()
		client := &http.Client{Timeout: 30 * time.Second}
		formats := []string{*videoFormat, *audioFormat}
		if *combinedFormat != "" {
			formats = []string{*combinedFormat}
		}
		for _, id := range formats {
			if urls[id] == "" {
				return errors.New("selected format is absent")
			}
			source, er := media.OpenHTTP(ctx, client, urls[id], info.ID+":"+id, store)
			if er != nil {
				return er
			}
			remote = append(remote, source)
		}
		v = remote[0]
		if len(remote) == 2 {
			a = remote[1]
		}
		videoID, channelID, title = info.ID, info.ChannelID, info.Title
		if info.Channel != "" {
			channelTitle = info.Channel
		}
		if published, e := time.Parse("20060102", info.UploadDate); e == nil {
			year = published.Year()
		}
	} else {
		if *combinedFormat != "" {
			return errors.New("--combined-format requires --source-json")
		}
		for _, p := range []string{*video, *audio} {
			if p == "" {
				return errors.New("both local tracks or --source-json are required")
			}
		}
		open := func(p string) (*local, error) {
			file, err := os.Open(p)
			if err != nil {
				return nil, err
			}
			st, err := file.Stat()
			if err != nil {
				file.Close()
				return nil, err
			}
			if !st.Mode().IsRegular() {
				file.Close()
				return nil, errors.New("fixture must be a regular file")
			}
			return &local{f: file, size: st.Size(), id: filepath.Base(p)}, nil
		}
		vl, err := open(*video)
		if err != nil {
			return err
		}
		defer vl.f.Close()
		al, err := open(*audio)
		if err != nil {
			return err
		}
		defer al.f.Close()
		v, a = vl, al
	}
	var virtual media.Representation = v
	var spanCount int
	profile := "finite-combined-mp4"
	if a != nil {
		muxed, err := media.Mux(ctx, v, a)
		if err != nil {
			return err
		}
		virtual, spanCount, profile = muxed, len(muxed.Spans), media.PackagingVersion
	}
	if *fixtureEpisodes < 1 || *fixtureEpisodes > 20 {
		return errors.New("fixture episode count must be between 1 and 20")
	}
	if *mountpoint != "" {
		makeEntries := func() ([]library.Entry, error) {
			episodes := []library.Episode{}
			if *fixtureCatalogue != "" {
				var items []struct {
					ID     string `json:"id"`
					Title  string `json:"title"`
					Year   int    `json:"year"`
					Number int    `json:"number"`
				}
				b, e := os.ReadFile(*fixtureCatalogue)
				if e != nil {
					return nil, e
				}
				if e = json.Unmarshal(b, &items); e != nil {
					return nil, e
				}
				for _, item := range items {
					episodes = append(episodes, library.Episode{ChannelID: channelID, ChannelTitle: channelTitle, VideoID: item.ID, Title: item.Title, Year: item.Year, Number: item.Number, Media: virtual})
				}
			} else {
				for i := 1; i <= *fixtureEpisodes; i++ {
					id, name := videoID, title
					if *fixtureEpisodes > 1 {
						id = fmt.Sprintf("%s-%d", videoID, i)
						name = fmt.Sprintf("%s %d", title, i)
					}
					episodes = append(episodes, library.Episode{ChannelID: channelID, ChannelTitle: channelTitle, VideoID: id, Title: name, Year: year, Number: i, Media: virtual})
				}
			}
			return library.Entries(episodes)
		}
		entries, er := makeEntries()
		if er != nil {
			return er
		}
		mount, er := library.MountReadOnly(*mountpoint, entries)
		if er != nil {
			return er
		}
		defer mount.Close()
		if *fixtureCatalogue != "" {
			go func() {
				initial, _ := os.ReadFile(*fixtureCatalogue)
				last := sha256.Sum256(initial)
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						data, e := os.ReadFile(*fixtureCatalogue)
						if e != nil {
							continue
						}
						checksum := sha256.Sum256(data)
						if checksum == last {
							continue
						}
						last = checksum
						if next, e := makeEntries(); e == nil {
							_ = mount.Update(next)
						}
					}
				}
			}()
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /media.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", fmt.Sprintf("%q", virtual.ID()))
		http.ServeContent(w, r, "media.mp4", time.Time{}, &media.Seeker{Context: r.Context(), Source: virtual})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "qualification slice ready\n") })
	mux.HandleFunc("GET /diagnostics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var upstream int64
		for _, s := range remote {
			upstream += s.UpstreamBytes.Load()
		}
		status := map[string]any{"mode": "qualification", "packaging_version": profile, "output_bytes": virtual.Size(), "spans": spanCount, "upstream_bytes": upstream, "combined_format": *combinedFormat}
		if store != nil {
			status["cache"] = store.Stats()
		}
		json.NewEncoder(w).Encode(status)
	})
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	slog.Info("qualification slice ready", "address", listener.Addr().String(), "output_bytes", virtual.Size(), "packaging_version", media.PackagingVersion)
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func checkPlex(args []string) error {
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
	result, err := c.Check(context.Background())
	if er := json.NewEncoder(os.Stdout).Encode(result); er != nil {
		return er
	}
	return err
}
