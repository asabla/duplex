package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fixtureChannel = "UCSMOQeBJ2RAnuFungnQOxLg"

func writeSourceFixture(t *testing.T, path, value string, mode os.FileMode) {
	t.Helper()

	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}

// The executable fixture tests actual process arguments, streaming, and cleanup.
func resolverFixture(t *testing.T) (*YouTube, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DUPLEX_PROCESS_FIXTURE", dir)

	script := `#!/bin/sh
set -eu
root="$DUPLEX_PROCESS_FIXTURE"
if test "$1" = --version; then cat "$root/version"; exit; fi
printf '%s\n' "$@" > "$root/arguments"
if test -f "$root/stall"; then exec sleep 30; fi
if test -f "$root/error"; then cat "$root/error" >&2; exit 2; fi
case "$*" in
 *"youtube:player_client=web;"*) if test -f "$root/metadata-response"; then cat "$root/metadata-response"; exit; fi ;;
esac
cat "$root/response"
`
	writeSourceFixture(t, filepath.Join(dir, "yt-dlp"), script, 0o700)
	writeSourceFixture(t, filepath.Join(dir, "deno"), "#!/bin/sh\ncat \"$DUPLEX_PROCESS_FIXTURE/deno-version\"\n", 0o700)
	writeSourceFixture(t, filepath.Join(dir, "version"), YTDLPVersion+"\n", 0o600)
	writeSourceFixture(t, filepath.Join(dir, "deno-version"), "deno "+DenoVersion+" (fixture)\n", 0o600)

	return &YouTube{Executable: filepath.Join(dir, "yt-dlp"), Deno: filepath.Join(dir, "deno")}, dir
}

func TestSourceProcessContract(t *testing.T) {
	y, dir := resolverFixture(t)
	if err := y.Check(t.Context()); err != nil {
		t.Fatal(err)
	}

	root := `{"channel_id":"` + fixtureChannel + `","channel":"Example","entries":[{"id":"aqz-KE-bpKQ"}]}`
	writeSourceFixture(t, filepath.Join(dir, "response"), root, 0o600)

	observed := Observed{SourceAdapter: y, Record: func(name string, d time.Duration) {
		if name == "" || d < 0 {
			t.Error("invalid measurement")
		}
	}}
	for _, call := range []func(context.Context, string) (Channel, error){observed.ResolveChannel, observed.Enumerate} {
		value := fixtureChannel
		// ResolveChannel accepts a URL; Enumerate accepts the canonical ID.
		if result, err := call(t.Context(), "https://youtube.com/channel/"+value); err != nil {
			if result, err = call(t.Context(), value); err != nil || result.ID != fixtureChannel {
				t.Fatal(result, err)
			}
		} else if result.ID != fixtureChannel {
			t.Fatal(result)
		}
	}

	reader, ok := CollectionReader(observed)
	if !ok || reader == nil {
		t.Fatal("observed adapter lost collection support")
	}

	writeSourceFixture(t, filepath.Join(dir, "response"), `{"id":"aqz-KE-bpKQ","channel_id":"`+fixtureChannel+`","title":"Recorded fixture","timestamp":1700000000,"duration":60,"live_status":"not_live"}`, 0o600)

	for _, call := range []func(context.Context, string) (Metadata, error){observed.Metadata, observed.Resolve} {
		result, err := call(t.Context(), "aqz-KE-bpKQ")
		if err != nil || result.State() != "recorded" {
			t.Fatal(result, err)
		}
	}

	args, err := os.ReadFile(filepath.Join(dir, "arguments")) //nolint:gosec // G304: This reads only the argument log generated in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	for _, required := range []string{"--ignore-config", "--no-remote-components", "--no-js-runtimes", "--js-runtimes", "--no-playlist", "youtube:formats=duplicate", "--\nhttps://www.youtube.com/watch?v=aqz-KE-bpKQ"} {
		if !strings.Contains(string(args), required) {
			t.Fatalf("missing required process argument %q", required)
		}
	}
}

func TestStreamingBatchesAndProcessFailures(t *testing.T) {
	y, dir := resolverFixture(t)

	var output strings.Builder
	for i := range 61 {
		fmt.Fprintf(&output, "{\"id\":\"video%06d\"}\n", i)
	}

	writeSourceFixture(t, filepath.Join(dir, "response"), output.String(), 0o600)

	sizes := []int{}

	listing, err := y.Entries(t.Context(), Scope{ChannelID: fixtureChannel, Tab: "videos", Start: 1}, func(batch []Entry) error {
		sizes = append(sizes, len(batch))
		if len(batch) > 25 || batch[0].Position < 1 {
			t.Error("unbounded or unordered batch")
		}

		return nil
	})
	if err != nil || listing.Count != 61 || !listing.Complete || fmt.Sprint(sizes) != "[25 25 11]" {
		t.Fatal(listing, sizes, err)
	}

	sentinel := errors.New("consumer stopped")
	if _, err = y.Entries(t.Context(), Scope{ChannelID: fixtureChannel, Tab: "videos"}, func([]Entry) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}

	writeSourceFixture(t, filepath.Join(dir, "error"), "private video: signed-secret\n", 0o600)

	if _, err = y.Metadata(t.Context(), "aqz-KE-bpKQ"); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "signed-secret") {
		t.Fatal(err)
	}

	writeSourceFixture(t, filepath.Join(dir, "error"), "does not have a streams tab", 0o600)

	empty, err := y.Entries(t.Context(), Scope{ChannelID: fixtureChannel, Tab: "streams"}, func([]Entry) error { t.Fatal("unexpected entry"); return nil })
	if err != nil || !empty.Complete || empty.Count != 0 {
		t.Fatal(empty, err)
	}

	if err = os.Remove(filepath.Join(dir, "error")); err != nil {
		t.Fatal(err)
	}

	writeSourceFixture(t, filepath.Join(dir, "stall"), "", 0o600)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	if _, err = y.Resolve(ctx, "aqz-KE-bpKQ"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestCollectionValidationAndBoundedSlots(t *testing.T) {
	y := &YouTube{}
	for _, scope := range []Scope{{PlaylistID: "../bad"}, {ChannelID: fixtureChannel, Tab: "other"}, {ChannelID: "invalid", Tab: "videos"}} {
		if _, err := y.Entries(t.Context(), scope, func([]Entry) error { return nil }); err == nil {
			t.Fatal("invalid scope accepted", scope)
		}
	}

	for _, listing := range []bool{false, true} {
		slots := 4
		if listing {
			slots = 2
		}

		release := []func(){}

		for range slots {
			done, err := y.acquire(t.Context(), listing)
			if err != nil {
				t.Fatal(err)
			}

			release = append(release, done)
		}

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		if _, err := y.acquire(ctx, listing); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}

		for _, done := range release {
			done()
		}
	}

	for _, value := range []string{"{", `{"id":"bad"}`, strings.Repeat("x", (2<<20)+1)} {
		if _, err := consumeEntries(t.Context(), bufio.NewScanner(strings.NewReader(value)), Scope{}, func([]Entry) error { return nil }); err == nil {
			t.Fatal("invalid stream accepted")
		}
	}

	buffer := &cappedBuffer{limit: 4}
	if n, err := buffer.Write([]byte("test")); err != nil || n != 4 {
		t.Fatal(n, err)
	}

	if _, err := buffer.Write([]byte("x")); err == nil {
		t.Fatal("response limit ignored")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := consumeEntries(ctx, bufio.NewScanner(bytes.NewBufferString(`{"id":"aqz-KE-bpKQ"}`)), Scope{}, func([]Entry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSourceVersionsAndPlaylistOwnership(t *testing.T) {
	y, dir := resolverFixture(t)
	writeSourceFixture(t, filepath.Join(dir, "response"), `{"id":"PLfixture0001","title":"Curated","thumbnails":[{"url":"https://i.ytimg.com/example.jpg","width":640,"height":360}]}`, 0o600)

	playlists, err := y.Playlists(t.Context(), fixtureChannel)
	if err != nil || len(playlists) != 1 || playlists[0].ChannelID != fixtureChannel || playlists[0].Thumbnail == "" {
		t.Fatal(playlists, err)
	}

	for _, value := range []string{"{", `null`, `{"id":"bad"}`} {
		writeSourceFixture(t, filepath.Join(dir, "response"), value, 0o600)

		if _, err = y.Playlists(t.Context(), fixtureChannel); err == nil {
			t.Fatal("partial playlist snapshot accepted")
		}
	}

	writeSourceFixture(t, filepath.Join(dir, "version"), "wrong", 0o600)

	if err = y.Check(t.Context()); err == nil {
		t.Fatal("unpinned resolver accepted")
	}

	writeSourceFixture(t, filepath.Join(dir, "version"), YTDLPVersion, 0o600)
	writeSourceFixture(t, filepath.Join(dir, "deno-version"), "deno 0", 0o600)

	if err = y.Check(t.Context()); err == nil {
		t.Fatal("unpinned runtime accepted")
	}

	y.Deno = filepath.Join(dir, "missing")
	if _, err = y.Resolve(t.Context(), "aqz-KE-bpKQ"); err == nil {
		t.Fatal("missing runtime accepted")
	}
}

func TestPlaylistRootStreamHasNoCollectionCountCap(t *testing.T) {
	y, dir := resolverFixture(t)

	var response strings.Builder
	for n := range 61 {
		fmt.Fprintf(&response, "{\"id\":\"PLfixture%06d\",\"title\":\"Collection %d\"}\n", n, n)
	}

	writeSourceFixture(t, filepath.Join(dir, "response"), response.String(), 0o600)

	counts := []int{}

	if err := y.StreamPlaylists(t.Context(), fixtureChannel, func(batch []Playlist) error { counts = append(counts, len(batch)); return nil }); err != nil {
		t.Fatal(err)
	}

	if fmt.Sprint(counts) != "[25 25 11]" {
		t.Fatal(counts)
	}

	failure := errors.New("root consumer failed")
	if err := y.StreamPlaylists(t.Context(), fixtureChannel, func([]Playlist) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func TestMetadataProfileFallsBackWithoutChangingIdentityOrDates(t *testing.T) {
	y, dir := resolverFixture(t)
	complete := `{"id":"aqz-KE-bpKQ","channel_id":"` + fixtureChannel + `","title":"Exact publication metadata","timestamp":1700000000,"duration":60,"live_status":"not_live","formats":[{"format_id":"video"}]}`
	writeSourceFixture(t, filepath.Join(dir, "response"), complete, 0o600)

	metadata, err := y.Metadata(t.Context(), "aqz-KE-bpKQ")
	if err != nil || metadata.Timestamp != 1700000000 || len(metadata.Formats) != 0 {
		t.Fatal(metadata, err)
	}

	arguments, err := os.ReadFile(filepath.Join(dir, "arguments")) //nolint:gosec // G304: This reads only a controlled process log in t.TempDir.
	if err != nil || !bytes.Contains(arguments, []byte("player_skip=js,configs")) {
		t.Fatal(string(arguments), err)
	}

	writeSourceFixture(t, filepath.Join(dir, "metadata-response"), `{"id":"aqz-KE-bpKQ","title":"Incomplete flat-like metadata","duration":60}`, 0o600)

	metadata, err = y.Metadata(t.Context(), "aqz-KE-bpKQ")
	if err != nil || metadata.ChannelID != fixtureChannel || metadata.Timestamp != 1700000000 || len(metadata.Formats) != 0 {
		t.Fatal(metadata, err)
	}

	arguments, err = os.ReadFile(filepath.Join(dir, "arguments")) //nolint:gosec // G304: This reads only a controlled process log in t.TempDir.
	if err != nil || bytes.Contains(arguments, []byte("player_skip=js,configs")) || !bytes.Contains(arguments, []byte("youtube:formats=duplicate")) {
		t.Fatal("incomplete metadata did not use full resolution", string(arguments), err)
	}

	writeSourceFixture(t, filepath.Join(dir, "metadata-response"), `{"id":"aqz-KE-bpKQ","availability":"private"}`, 0o600)

	if _, err = y.Metadata(t.Context(), "aqz-KE-bpKQ"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}

	writeSourceFixture(t, filepath.Join(dir, "metadata-response"), `{"id":"k0ILMhbWHNQ"}`, 0o600)

	if _, err = y.Metadata(t.Context(), "aqz-KE-bpKQ"); err == nil {
		t.Fatal("changed source identity accepted")
	}

	if _, err = y.Metadata(t.Context(), "invalid"); err == nil {
		t.Fatal("invalid source ID accepted")
	}
}
