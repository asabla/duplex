package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// YouTube runs the pinned resolver with separate bounded listing and metadata slots.
type YouTube struct {
	Executable    string
	Deno          string
	init          sync.Once
	metadataSlots chan struct{}
	listingSlots  chan struct{}
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("resolver response too large")
	}

	return b.Buffer.Write(p)
}

func (y *YouTube) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	release, err := y.acquire(ctx, false)
	if err != nil {
		return nil, err
	}
	defer release()

	cmd, err := y.command(ctx, args, true)
	if err != nil {
		return nil, err
	}

	out := &cappedBuffer{limit: 64 << 20}
	cmd.Stdout = out
	stderr := &cappedBuffer{limit: 64 << 10}

	cmd.Stderr = stderr
	if e := cmd.Run(); e != nil {
		return nil, resolverError(ctx, stderr.String())
	}

	return out.Bytes(), nil
}

// Check verifies the resolver, EJS component, and JavaScript runtime contract.
func (y *YouTube) Check(ctx context.Context) error {
	binary := y.Executable
	if binary == "" {
		binary = "yt-dlp"
	}

	b, e := exec.CommandContext(ctx, binary, "--version").Output() //nolint:gosec // G204: The operator selects the pinned resolver; arguments are fixed.
	if e != nil {
		return errors.New("yt-dlp could not start; check its runtime installation")
	}

	if strings.TrimSpace(string(b)) != YTDLPVersion {
		return errors.New("yt-dlp version differs from pinned release")
	}

	deno := y.Deno
	if deno == "" {
		deno = "deno"
	}

	b, e = exec.CommandContext(ctx, deno, "--version").Output() //nolint:gosec // G204: The operator selects the pinned JavaScript runtime; arguments are fixed.
	if e != nil {
		return errors.New("deno could not start; check its runtime installation")
	}

	if !strings.HasPrefix(string(b), "deno "+DenoVersion+" ") {
		return errors.New("deno version differs from pinned release")
	}

	return nil
}

func (y *YouTube) scanProcess(ctx context.Context, args []string, consume func(context.Context, *bufio.Scanner) (int, error)) (int, error) {
	release, err := y.acquire(ctx, true)
	if err != nil {
		return 0, err
	}
	defer release()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	command, err := y.command(ctx, args, false)
	if err != nil {
		return 0, err
	}

	stderr := &cappedBuffer{limit: 64 << 10}
	command.Stderr = stderr

	pipe, err := command.StdoutPipe()
	if err != nil {
		return 0, err
	}

	if err = command.Start(); err != nil {
		return 0, errors.Join(err, pipe.Close())
	}

	count, readErr := consume(ctx, bufio.NewScanner(pipe))
	if readErr != nil {
		cancel()
	}

	waitErr := command.Wait()

	if readErr != nil {
		return count, readErr
	}

	if waitErr != nil {
		return count, resolverError(ctx, stderr.String())
	}

	return count, nil
}

func (y *YouTube) command(ctx context.Context, args []string, single bool) (*exec.Cmd, error) {
	binary := y.Executable
	if binary == "" {
		binary = "yt-dlp"
	}

	deno := y.Deno
	if deno == "" {
		deno = "deno"
	}

	path, err := exec.LookPath(deno)
	if err != nil {
		return nil, errors.New("deno is unavailable")
	}

	base := []string{"--ignore-config", "--no-warnings", "--no-progress", "--ignore-no-formats-error", "--no-remote-components", "--no-js-runtimes", "--js-runtimes", "deno:" + path, "--socket-timeout", "20", "--retries", "1", "--extractor-retries", "1", "--skip-download", "--abort-on-error"}
	if single {
		base = append(base, "--dump-single-json")
	}

	command := exec.CommandContext(ctx, binary, append(base, args...)...) //nolint:gosec // G204: The operator selects the pinned executable; source URLs are validated and passed as arguments without a shell.
	command.WaitDelay = 5 * time.Second

	return command, nil
}

func resolverError(ctx context.Context, stderr string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	message := strings.ToLower(stderr)
	for _, tab := range []string{"videos", "streams", "shorts", "playlists"} {
		if strings.Contains(message, "does not have a "+tab+" tab") || strings.Contains(message, "doesn't have a "+tab+" tab") {
			return ErrMissingCollection
		}
	}

	for _, marker := range []string{"private video", "members-only", "video unavailable", "video is not available"} {
		if strings.Contains(message, marker) {
			return ErrUnavailable
		}
	}

	return errors.New("YouTube source request failed")
}

func (y *YouTube) acquire(ctx context.Context, listing bool) (func(), error) {
	y.init.Do(func() { y.metadataSlots = make(chan struct{}, 4); y.listingSlots = make(chan struct{}, 2) })

	slots := y.metadataSlots
	if listing {
		slots = y.listingSlots
	}

	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
